package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type chatService struct {
	projects *projectService
	mu       sync.Mutex
	cancels  map[string]context.CancelFunc
	done     map[string]chan struct{}
}

type runProgressEvent struct {
	StepID string `json:"step_id,omitempty"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Status string `json:"status,omitempty"`
}

type codexFileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

func newChatService(p *projectService) *chatService {
	return &chatService{projects: p, cancels: make(map[string]context.CancelFunc), done: make(map[string]chan struct{})}
}
func (s *chatService) messages(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(currentUserKey{}).(User)
	p, e := s.projects.byID(r.Context(), u.ID, chi.URLParam(r, "id"))
	if e == pgx.ErrNoRows {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return
	}
	if e != nil {
		apiError(w, 500, "PROJECT_READ_FAILED")
		return
	}
	rows, e := s.projects.db.Query(r.Context(), `SELECT id,role,content,selected_ui,created_at FROM messages WHERE project_id=$1 ORDER BY created_at`, p.ID)
	if e != nil {
		apiError(w, 500, "MESSAGE_READ_FAILED")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, role, content string
		var selected any
		var created any
		if rows.Scan(&id, &role, &content, &selected, &created) == nil {
			out = append(out, map[string]any{"id": id, "role": role, "content": content, "selected_ui": selected, "created_at": created})
		}
	}
	writeJSON(w, 200, map[string]any{"messages": out})
}
func (s *chatService) send(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Content    string `json:"content"`
		SelectedUI any    `json:"selected_ui"`
	}
	if decodeJSON(r, &in) != nil || strings.TrimSpace(in.Content) == "" || len([]rune(in.Content)) > 20000 {
		apiError(w, 400, "INVALID_MESSAGE")
		return
	}
	in.Content = strings.TrimSpace(in.Content)
	u := r.Context().Value(currentUserKey{}).(User)
	p, e := s.projects.byID(r.Context(), u.ID, chi.URLParam(r, "id"))
	if e != nil {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return
	}
	mid, rid := newID(), newID()
	tx, e := s.projects.db.Begin(r.Context())
	if e != nil {
		apiError(w, 500, "RUN_CREATE_FAILED")
		return
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, p.ID); e != nil {
		apiError(w, 500, "RUN_CREATE_FAILED")
		return
	}
	var active bool
	if e = tx.QueryRow(r.Context(), `SELECT `+projectBusySQL, p.ID).Scan(&active); e != nil {
		apiError(w, 500, "RUN_READ_FAILED")
		return
	}
	if active {
		apiError(w, 409, "AGENT_RUN_IN_PROGRESS")
		return
	}
	owned, e := projectOwned(r.Context(), tx, p.ID, u.ID)
	if e != nil || !owned {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return
	}
	if e = s.projects.ensureBaseline(r.Context(), tx, p); e != nil {
		apiError(w, 500, "VERSION_SAVE_FAILED")
		return
	}
	if _, e = tx.Exec(r.Context(), `UPDATE projects SET source_revision=source_revision+1 WHERE id=$1`, p.ID); e != nil {
		apiError(w, 500, "RUN_CREATE_FAILED")
		return
	}
	_, e = tx.Exec(r.Context(), `INSERT INTO messages(id,project_id,role,content,selected_ui) VALUES($1,$2,'user',$3,$4)`, mid, p.ID, in.Content, in.SelectedUI)
	if e == nil {
		_, e = tx.Exec(r.Context(), `INSERT INTO agent_runs(id,project_id,status,user_message_id,started_at) VALUES($1,$2,'PENDING',$3,now())`, rid, p.ID, mid)
	}
	if e != nil {
		if activeRunConflict(e) {
			apiError(w, 409, "AGENT_RUN_IN_PROGRESS")
			return
		}
		apiError(w, 500, "RUN_CREATE_FAILED")
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		if activeRunConflict(e) {
			apiError(w, 409, "AGENT_RUN_IN_PROGRESS")
			return
		}
		apiError(w, 500, "RUN_CREATE_FAILED")
		return
	}
	runCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancels[rid] = cancel
	s.done[rid] = make(chan struct{})
	s.mu.Unlock()
	go func() {
		defer s.unregisterRun(rid)
		s.run(runCtx, p, u.ID, rid, in.Content, in.SelectedUI)
	}()
	writeJSON(w, 202, map[string]string{"run_id": rid})
}
func activeRunConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "agent_runs_one_active_per_project"
}
func (s *chatService) unregisterRun(runID string) {
	s.mu.Lock()
	delete(s.cancels, runID)
	if done := s.done[runID]; done != nil {
		close(done)
		delete(s.done, runID)
	}
	s.mu.Unlock()
}
func (s *chatService) cancelLocalRun(runID string) {
	s.mu.Lock()
	cancel := s.cancels[runID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (s *chatService) run(ctx context.Context, p Project, userID, runID, prompt string, selected any) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	tag, err := s.projects.db.Exec(ctx, `UPDATE agent_runs SET status='RUNNING' WHERE id=$1 AND status='PENDING'`, runID)
	if err != nil || tag.RowsAffected() == 0 {
		return
	}
	s.progress(ctx, runID, "正在读取模型配置…")
	var base, model string
	var sealed []byte
	err = s.projects.db.QueryRow(ctx, `SELECT base_url,api_key_ciphertext,model FROM llm_configs WHERE user_id=$1`, userID).Scan(&base, &sealed, &model)
	if err != nil {
		s.fail(runID, "Model configuration is required.")
		return
	}
	key, err := decrypt(s.projects.cfg.MasterKey, sealed)
	if err != nil {
		s.fail(runID, "Model configuration could not be read.")
		return
	}
	if err = s.projects.ensureRuntime(ctx, p); err != nil {
		s.fail(runID, "Runtime could not be started.")
		return
	}
	s.progress(ctx, runID, "正在准备隔离运行环境…")
	config := fmt.Sprintf("model_provider = \"atoms\"\nmodel = %q\n[model_providers.atoms]\nname = \"Atoms\"\nbase_url = %q\nenv_key = \"OPENAI_API_KEY\"\nwire_api = \"responses\"\n", model, strings.TrimRight(base, "/"))
	if err = os.WriteFile(filepath.Join(p.CodexStatePath, "config.toml"), []byte(config), 0600); err != nil {
		s.fail(runID, "Session configuration failed.")
		return
	}
	if os.Geteuid() == 0 {
		_ = os.Chown(filepath.Join(p.CodexStatePath, "config.toml"), 1000, 1000)
	}
	task := prompt
	if selected != nil {
		selectedJSON, marshalErr := json.Marshal(selected)
		if marshalErr != nil {
			s.fail(runID, "Selected UI context could not be encoded.")
			return
		}
		task = "The user selected a rendered UI element. Locate its implementation and make the smallest reasonable change.\n\nSelected UI context:\n" + string(selectedJSON) + "\n\nUser request:\n" + prompt
	}
	cmd := []string{"codex", "exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-C", "/workspace"}
	if hasSession(p.CodexStatePath) {
		cmd = append(cmd, "resume", "--last")
	}
	cmd = append(cmd, task)
	s.progress(ctx, runID, "正在分析并修改项目…")
	lastProgress := ""
	out, err := s.projects.docker.execStream(ctx, runtimeName(p.ID), cmd, []string{"CODEX_HOME=/codex", "OPENAI_API_KEY=" + key}, func(line []byte) {
		event, ok := codexProgress(line, key)
		if ok {
			key := event.progressKey()
			if key != lastProgress {
				lastProgress = key
				s.progressEvent(ctx, runID, event)
			}
		}
	})
	// Execution logs are retained for diagnosis but must never persist the provider key.
	redacted := []byte(strings.ReplaceAll(string(out), key, "[REDACTED]"))
	_ = os.WriteFile(filepath.Join(filepath.Dir(p.WorkspacePath), "logs", runID+".ndjson"), redacted, 0600)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			s.fail(runID, "Task timed out.")
			return
		}
		s.fail(runID, "Application update failed.")
		return
	}
	for repair := 0; repair <= 2; repair++ {
		if !s.transitionRun(ctx, runID, "VERIFYING") {
			if ctx.Err() == nil {
				s.fail(runID, "Run state could not be saved.")
			}
			return
		}
		s.progress(ctx, runID, "正在验证类型、测试、构建和预览…")
		if verifyErr := s.verify(ctx, p, runID); verifyErr == nil {
			break
		} else if repair == 2 {
			s.fail(runID, "Verification failed after repair attempts.")
			return
		} else {
			if !s.transitionRun(ctx, runID, "REPAIRING") {
				if ctx.Err() == nil {
					s.fail(runID, "Run state could not be saved.")
				}
				return
			}
			s.progress(ctx, runID, "验证未通过，正在自动修复…")
			if err := s.projects.ensureRuntime(ctx, p); err != nil {
				s.fail(runID, "Runtime could not be restarted for repair.")
				return
			}
			fix := []string{"codex", "exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-C", "/workspace", "resume", "--last", "Fix the verification failure: " + verifyErr.Error() + ". Then rerun the required checks."}
			if _, err := s.projects.docker.exec(ctx, runtimeName(p.ID), fix, []string{"CODEX_HOME=/codex", "OPENAI_API_KEY=" + key}); err != nil {
				if errors.Is(ctx.Err(), context.Canceled) {
					return
				}
				s.fail(runID, "Automatic repair failed.")
				return
			}
		}
	}
	summary := extractSummary(out)
	if summary == "" {
		summary = "应用更新完成。"
	}
	s.complete(runID, p, summary, prompt)
}

func (s *chatService) transitionRun(ctx context.Context, runID, status string) bool {
	tag, err := s.projects.db.Exec(ctx, `UPDATE agent_runs SET status=$2 WHERE id=$1 AND status IN ('PENDING','RUNNING','VERIFYING','REPAIRING')`, runID, status)
	return err == nil && tag.RowsAffected() > 0
}
func (s *chatService) verify(ctx context.Context, p Project, runID string) error {
	checks := []struct {
		id      string
		title   string
		command string
	}{
		{"install", "同步项目依赖", "pnpm install --frozen-lockfile=false --prefer-offline"},
		{"typecheck", "TypeScript 类型检查", "pnpm typecheck"},
		{"test", "自动化测试", "pnpm test"},
		{"build", "生产版本构建", "pnpm build"},
	}
	for _, check := range checks {
		event := runProgressEvent{StepID: "verify-" + check.id, Kind: "verify", Title: check.title, Detail: check.command, Status: "running"}
		s.progressEvent(ctx, runID, event)
		if _, err := s.projects.docker.exec(ctx, runtimeName(p.ID), []string{"sh", "-lc", check.command}, nil); err != nil {
			event.Status = "failed"
			event.Detail = "执行失败：" + check.command
			s.progressEvent(ctx, runID, event)
			return fmt.Errorf("%s", check.command)
		}
		event.Status = "completed"
		s.progressEvent(ctx, runID, event)
	}
	restart := runProgressEvent{StepID: "verify-preview-restart", Kind: "verify", Title: "重启预览服务", Detail: "使用已验证的项目文件重建运行环境", Status: "running"}
	s.progressEvent(ctx, runID, restart)
	if err := s.projects.recreateRuntime(ctx, p); err != nil {
		restart.Status = "failed"
		restart.Detail = "运行环境重建失败"
		s.progressEvent(ctx, runID, restart)
		return fmt.Errorf("restart preview: %w", err)
	}
	if err := waitRuntimeReady(ctx, p.ID); err != nil {
		restart.Status = "failed"
		restart.Detail = "预览服务未能按时就绪"
		s.progressEvent(ctx, runID, restart)
		return fmt.Errorf("preview readiness: %w", err)
	}
	restart.Status = "completed"
	s.progressEvent(ctx, runID, restart)

	smoke := runProgressEvent{StepID: "verify-smoke", Kind: "verify", Title: "预览页面检查", Detail: "GET /", Status: "running"}
	s.progressEvent(ctx, runID, smoke)
	if _, err := s.projects.docker.exec(ctx, runtimeName(p.ID), []string{"sh", "-lc", "curl -LfsS http://127.0.0.1:3000/ >/dev/null"}, nil); err != nil {
		smoke.Status = "failed"
		smoke.Detail = "预览首页请求失败"
		s.progressEvent(ctx, runID, smoke)
		return fmt.Errorf("preview smoke: %w", err)
	}
	smoke.Status = "completed"
	s.progressEvent(ctx, runID, smoke)
	return nil
}
func (s *chatService) fail(id, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var projectID string
	if err := s.projects.db.QueryRow(ctx, `SELECT project_id FROM agent_runs WHERE id=$1`, id).Scan(&projectID); err != nil {
		return
	}
	tx, err := s.projects.db.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	if err = projectLock(ctx, tx, projectID); err != nil {
		return
	}
	tag, err := tx.Exec(ctx, `UPDATE agent_runs SET status='CANCELLING',error_message=$2 WHERE id=$1 AND status IN ('PENDING','RUNNING','VERIFYING','REPAIRING')`, id, msg)
	if err != nil || tag.RowsAffected() == 0 {
		return
	}
	if err = tx.Commit(ctx); err != nil {
		return
	}
	s.cancelLocalRun(id)
	if err = s.projects.restoreRuntime().Stop(ctx, Project{ID: projectID}); err != nil {
		s.progress(ctx, id, "任务清理未完成，项目已保护，请检查运行环境后重启服务。")
		return
	}
	tag, err = s.projects.db.Exec(ctx, `UPDATE agent_runs SET status='FAILED',error_message=$2,finished_at=now() WHERE id=$1 AND status='CANCELLING'`, id, msg)
	if err == nil && tag.RowsAffected() > 0 {
		s.progress(ctx, id, msg)
	}
}
func (s *chatService) complete(runID string, p Project, summary, prompt string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	thumbnail := s.projects.restoreRuntime().Capture(ctx, p)
	saved, err := func() (bool, error) {
		tx, err := s.projects.db.Begin(ctx)
		if err != nil {
			return false, err
		}
		defer tx.Rollback(ctx)
		if err = projectLock(ctx, tx, p.ID); err != nil {
			return false, err
		}
		var active bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE id=$1 AND status IN ('PENDING','RUNNING','VERIFYING','REPAIRING'))`, runID).Scan(&active); err != nil {
			return false, err
		}
		if !active {
			return false, nil
		}
		if _, err = s.projects.saveVersion(ctx, tx, p, prompt, &runID, thumbnail); err != nil {
			return false, err
		}
		tag, err := tx.Exec(ctx, `UPDATE agent_runs SET status='COMPLETED',finished_at=now() WHERE id=$1 AND status IN ('PENDING','RUNNING','VERIFYING','REPAIRING')`, runID)
		if err != nil {
			return false, err
		}
		if tag.RowsAffected() == 0 {
			return false, nil
		}
		if _, err = tx.Exec(ctx, `INSERT INTO messages(id,project_id,role,content) VALUES($1,$2,'assistant',$3)`, newID(), p.ID, summary); err != nil {
			return false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return true, nil
	}()
	if err != nil {
		s.fail(runID, "版本或任务结果保存失败，请重试。")
		return
	}
	if saved {
		s.progress(ctx, runID, "已完成。")
		if err := s.projects.cleanVersionStore(ctx, p); err != nil {
			log.Printf("version cleanup deferred project_id=%s", p.ID)
		}
	}
}
func (s *chatService) progress(ctx context.Context, runID, message string) {
	s.progressEvent(ctx, runID, runProgressEvent{Kind: "info", Title: publicProgressText(message)})
}
func (s *chatService) progressEvent(ctx context.Context, runID string, event runProgressEvent) {
	event.Title = strings.TrimSpace(event.Title)
	event.Detail = strings.TrimSpace(event.Detail)
	if event.Title == "" {
		return
	}
	if event.Kind == "" {
		event.Kind = "info"
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = s.projects.db.Exec(ctx, `INSERT INTO agent_run_events(run_id,content) VALUES($1,$2)`, runID, string(raw))
}
func (event runProgressEvent) progressKey() string {
	return event.StepID + "\x00" + event.Kind + "\x00" + event.Title + "\x00" + event.Detail + "\x00" + event.Status
}
func codexProgress(line []byte, secrets ...string) (runProgressEvent, bool) {
	var event struct {
		Type string `json:"type"`
		Item struct {
			ID               string            `json:"id"`
			Type             string            `json:"type"`
			Text             string            `json:"text"`
			Message          string            `json:"message"`
			Command          string            `json:"command"`
			AggregatedOutput string            `json:"aggregated_output"`
			ExitCode         *int              `json:"exit_code"`
			Status           string            `json:"status"`
			Changes          []codexFileChange `json:"changes"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &event) != nil {
		return runProgressEvent{}, false
	}
	switch event.Type {
	case "thread.started", "turn.started":
		return runProgressEvent{StepID: "run-start", Kind: "analysis", Title: "开始处理请求", Status: "running"}, true
	case "item.started", "item.updated", "item.completed":
		status := progressStatus(event.Type, event.Item.Status, event.Item.ExitCode)
		switch event.Item.Type {
		case "reasoning":
			if event.Type == "item.updated" {
				return runProgressEvent{}, false
			}
			return runProgressEvent{StepID: event.Item.ID, Kind: "analysis", Title: "分析实现方案", Status: status}, true
		case "command_execution":
			title, detail := describeCommand(event.Item.Command)
			if status == "failed" && event.Item.ExitCode != nil {
				detail = appendDetail(detail, fmt.Sprintf("退出码 %d", *event.Item.ExitCode))
				if title == "检查 TypeScript 类型" || title == "运行自动化测试" || title == "构建生产版本" {
					detail = appendDetail(detail, safeCommandOutput(event.Item.AggregatedOutput, secrets...))
				}
			}
			return runProgressEvent{StepID: event.Item.ID, Kind: "command", Title: title, Detail: detail, Status: status}, true
		case "file_change":
			title, detail := describeFileChanges(event.Item.Changes)
			return runProgressEvent{StepID: event.Item.ID, Kind: "file", Title: title, Detail: detail, Status: status}, title != ""
		case "agent_message":
			if event.Type == "item.completed" && strings.TrimSpace(event.Item.Text) != "" {
				return runProgressEvent{StepID: event.Item.ID, Kind: "message", Title: truncateProgress(publicProgressText(event.Item.Text), 220), Status: "completed"}, true
			}
		case "error":
			if event.Type == "item.completed" && strings.TrimSpace(event.Item.Message) != "" {
				if strings.Contains(strings.ToLower(event.Item.Message), "defaulting to fallback metadata") {
					return runProgressEvent{}, false
				}
				return runProgressEvent{StepID: event.Item.ID, Kind: "error", Title: truncateProgress(publicProgressText(event.Item.Message), 220), Status: "failed"}, true
			}
		}
	}
	return runProgressEvent{}, false
}
func progressStatus(eventType, itemStatus string, exitCode *int) string {
	if eventType == "item.started" || itemStatus == "in_progress" {
		return "running"
	}
	if exitCode != nil && *exitCode != 0 || itemStatus == "failed" {
		return "failed"
	}
	return "completed"
}
func describeCommand(command string) (string, string) {
	detail := safeCommandDetail(command)
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "pnpm typecheck") || strings.Contains(lower, "tsc --noemit") || strings.Contains(lower, "tsc --no-emit"):
		return "检查 TypeScript 类型", detail
	case strings.Contains(lower, "pnpm test") || strings.Contains(lower, "vitest"):
		return "运行自动化测试", detail
	case strings.Contains(lower, "pnpm build") || strings.Contains(lower, "next build"):
		return "构建生产版本", detail
	case strings.Contains(lower, "curl ") || strings.Contains(lower, "wget "):
		return "检查预览页面", detail
	case strings.Contains(lower, "mkdir "):
		return "创建项目目录", detail
	case strings.Contains(lower, "cat >") || strings.Contains(lower, "apply_patch") || strings.Contains(lower, " tee "):
		return "写入项目文件", detail
	case strings.HasPrefix(lower, "pwd") || strings.Contains(lower, " ls ") || strings.HasPrefix(lower, "ls ") || strings.Contains(lower, "find ") || strings.Contains(lower, " cat ") || strings.HasPrefix(lower, "cat ") || strings.Contains(lower, "sed ") || strings.Contains(lower, "rg ") || strings.Contains(lower, "grep "):
		return "检查项目文件", detail
	case detail == "":
		return "执行项目命令", ""
	default:
		return "执行项目命令", detail
	}
}
func safeCommandDetail(command string) string {
	command = strings.TrimSpace(command)
	for _, prefix := range []string{"/bin/sh -lc ", "sh -lc "} {
		command = strings.TrimPrefix(command, prefix)
	}
	command = strings.TrimSpace(command)
	if len(command) >= 2 && ((command[0] == '\'' && command[len(command)-1] == '\'') || (command[0] == '"' && command[len(command)-1] == '"')) {
		command = command[1 : len(command)-1]
	}
	if index := strings.Index(command, "<<"); index >= 0 {
		command = strings.TrimSpace(command[:index]) + " << …"
	}
	if index := strings.IndexByte(command, '\n'); index >= 0 {
		command = command[:index] + " …"
	}
	command = strings.ReplaceAll(command, "/workspace/", "")
	command = strings.ReplaceAll(command, "/workspace", ".")
	if containsSensitive(command) {
		return "受保护的项目命令"
	}
	return truncateProgress(publicProgressText(command), 180)
}
func safeCommandOutput(output string, secrets ...string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" || containsSensitive(line, secrets...) {
			continue
		}
		return "错误摘要：" + truncateProgress(publicProgressText(line), 180)
	}
	return ""
}
func containsSensitive(value string, secrets ...string) bool {
	lower := strings.ToLower(value)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(value, secret) {
			return true
		}
	}
	for _, marker := range []string{"api_key", "api key", "authorization", "password", "secret", "access_token", "database_url", "postgres://", "dbname=", "password=", "sk-"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
func describeFileChanges(changes []codexFileChange) (string, string) {
	if len(changes) == 0 {
		return "更新项目文件", ""
	}
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		path := strings.TrimPrefix(filepath.Clean(change.Path), "/workspace/")
		verb := map[string]string{"add": "新增", "create": "新增", "update": "修改", "delete": "删除"}[change.Kind]
		if verb == "" {
			verb = "更新"
		}
		paths = append(paths, verb+" "+path)
	}
	if len(paths) == 1 {
		return paths[0], ""
	}
	detailPaths := paths
	if len(detailPaths) > 5 {
		detailPaths = append(detailPaths[:5], fmt.Sprintf("另有 %d 个文件", len(paths)-5))
	}
	return fmt.Sprintf("更新 %d 个项目文件", len(paths)), strings.Join(detailPaths, " · ")
}
func appendDetail(current, extra string) string {
	if strings.TrimSpace(extra) == "" {
		return current
	}
	if strings.TrimSpace(current) == "" {
		return extra
	}
	return current + " · " + extra
}
func publicProgressText(value string) string {
	return strings.NewReplacer("Codex", "系统", "codex", "系统").Replace(strings.TrimSpace(value))
}
func truncateProgress(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}
func hasSession(root string) bool {
	found := false
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".jsonl") {
			found = true
			return filepath.SkipDir
		}
		return nil
	})
	return found
}
func (s *chatService) runInfo(w http.ResponseWriter, r *http.Request) {
	run, ok := s.ownedRun(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, run)
}
func (s *chatService) activeRun(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(currentUserKey{}).(User)
	var v runView
	err := s.projects.db.QueryRow(r.Context(), `
		SELECT r.id,r.project_id,r.status,r.error_message,r.started_at,r.finished_at
		FROM agent_runs r
		JOIN projects p ON p.id=r.project_id
		WHERE r.project_id=$1 AND p.user_id=$2
		  AND r.status IN ('PENDING','RUNNING','VERIFYING','REPAIRING','CANCELLING')
		ORDER BY r.started_at DESC
		LIMIT 1
	`, chi.URLParam(r, "id"), u.ID).Scan(&v.ID, &v.ProjectID, &v.Status, &v.ErrorMessage, &v.StartedAt, &v.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"run": nil})
		return
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, "RUN_READ_FAILED")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": v})
}
func (s *chatService) events(w http.ResponseWriter, r *http.Request) {
	_, ok := s.ownedRun(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	f, ok := w.(http.Flusher)
	if !ok {
		apiError(w, 500, "SSE_UNAVAILABLE")
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastEventID int64
	if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			lastEventID = parsed
		}
	}
	for {
		run, exists := s.ownedRun(w, r)
		if !exists {
			return
		}
		rows, err := s.projects.db.Query(r.Context(), `SELECT id,content,created_at FROM agent_run_events WHERE run_id=$1 AND id>$2 ORDER BY id`, run.ID, lastEventID)
		if err == nil {
			for rows.Next() {
				var id int64
				var content string
				var createdAt any
				if rows.Scan(&id, &content, &createdAt) == nil {
					lastEventID = id
					payload := map[string]any{"id": id, "content": content, "created_at": createdAt, "kind": "info", "title": content}
					var event runProgressEvent
					if json.Unmarshal([]byte(content), &event) == nil && strings.TrimSpace(event.Title) != "" {
						payload["step_id"] = event.StepID
						payload["kind"] = event.Kind
						payload["title"] = event.Title
						payload["detail"] = event.Detail
						payload["status"] = event.Status
						payload["content"] = event.Title
					}
					raw, _ := json.Marshal(payload)
					fmt.Fprintf(w, "id: %d\nevent: progress\ndata: %s\n\n", id, raw)
				}
			}
			rows.Close()
		}
		raw, _ := json.Marshal(run)
		fmt.Fprintf(w, "event: status\ndata: %s\n\n", raw)
		f.Flush()
		if terminal(run.Status) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *chatService) cancel(w http.ResponseWriter, r *http.Request) {
	run, ok := s.ownedRun(w, r)
	if !ok {
		return
	}
	if terminal(run.Status) {
		apiError(w, 409, "RUN_NOT_ACTIVE")
		return
	}
	tx, e := s.projects.db.Begin(r.Context())
	if e != nil {
		apiError(w, 500, "RUN_CANCEL_FAILED")
		return
	}
	defer tx.Rollback(r.Context())
	if e = projectLock(r.Context(), tx, run.ProjectID); e != nil {
		apiError(w, 500, "RUN_CANCEL_FAILED")
		return
	}
	tag, e := tx.Exec(r.Context(), `UPDATE agent_runs SET status='CANCELLING' WHERE id=$1 AND status IN ('PENDING','RUNNING','VERIFYING','REPAIRING')`, run.ID)
	if e != nil {
		apiError(w, 500, "RUN_CANCEL_FAILED")
		return
	}
	if tag.RowsAffected() == 0 {
		apiError(w, http.StatusConflict, "RUN_NOT_ACTIVE")
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		apiError(w, 500, "RUN_CANCEL_FAILED")
		return
	}
	s.cancelLocalRun(run.ID)
	resetCtx, resetCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer resetCancel()
	runtimeReset := true
	if err := s.projects.restoreRuntime().Stop(resetCtx, Project{ID: run.ProjectID}); err != nil {
		runtimeReset = false
		log.Printf("cancel runtime cleanup failed run_id=%s project_id=%s: %v", run.ID, run.ProjectID, err)
	}
	s.mu.Lock()
	done := s.done[run.ID]
	s.mu.Unlock()
	if runtimeReset && done != nil {
		select {
		case <-done:
		case <-resetCtx.Done():
			runtimeReset = false
		}
	}
	if !runtimeReset {
		s.progress(resetCtx, run.ID, "正在清理取消的任务，项目暂不可修改。")
		writeJSON(w, 202, map[string]any{"status": "CANCELLING", "runtime_reset": false})
		return
	}
	if _, e = s.projects.db.Exec(resetCtx, `UPDATE agent_runs SET status='CANCELLED',finished_at=now() WHERE id=$1 AND status='CANCELLING'`, run.ID); e != nil {
		apiError(w, 500, "RUN_CANCEL_FAILED")
		return
	}
	writeJSON(w, 200, map[string]any{"status": "CANCELLED", "runtime_reset": true})
}

type runView struct {
	ID           string  `json:"id"`
	ProjectID    string  `json:"-"`
	Status       string  `json:"status"`
	ErrorMessage *string `json:"error_message,omitempty"`
	StartedAt    any     `json:"started_at"`
	FinishedAt   any     `json:"finished_at"`
}

func (s *chatService) ownedRun(w http.ResponseWriter, r *http.Request) (runView, bool) {
	u := r.Context().Value(currentUserKey{}).(User)
	var v runView
	e := s.projects.db.QueryRow(r.Context(), `SELECT r.id,r.project_id,r.status,r.error_message,r.started_at,r.finished_at FROM agent_runs r JOIN projects p ON p.id=r.project_id WHERE r.id=$1 AND p.user_id=$2`, chi.URLParam(r, "id"), u.ID).Scan(&v.ID, &v.ProjectID, &v.Status, &v.ErrorMessage, &v.StartedAt, &v.FinishedAt)
	if e != nil {
		apiError(w, 404, "RUN_NOT_FOUND")
		return runView{}, false
	}
	return v, true
}
func (s *chatService) recoverInterruptedRuns(ctx context.Context) error {
	rows, err := s.projects.db.Query(ctx, `SELECT DISTINCT project_id FROM agent_runs WHERE status IN ('PENDING','RUNNING','VERIFYING','REPAIRING','CANCELLING')`)
	if err != nil {
		return err
	}
	projects := map[string]struct{}{}
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			rows.Close()
			return err
		}
		projects[projectID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for projectID := range projects {
		cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := s.projects.removeRuntime(cleanupCtx, projectID)
		cancel()
		if err != nil {
			return fmt.Errorf("remove interrupted runtime %s: %w", projectID, err)
		}
		if _, err = s.projects.db.Exec(ctx, `UPDATE agent_runs SET status='FAILED',error_message='服务重启导致任务中断，请重新提交。',finished_at=now() WHERE project_id=$1 AND status IN ('PENDING','RUNNING','VERIFYING','REPAIRING','CANCELLING')`, projectID); err != nil {
			return err
		}
	}
	return nil
}
func (s *chatService) shutdown(ctx context.Context) error {
	s.mu.Lock()
	for _, cancel := range s.cancels {
		cancel()
	}
	s.mu.Unlock()
	return s.recoverInterruptedRuns(ctx)
}
func terminal(s string) bool { return s == "COMPLETED" || s == "FAILED" || s == "CANCELLED" }
func extractSummary(raw []byte) string {
	var last string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Type == "item.completed" && ev.Item.Type == "agent_message" && strings.TrimSpace(ev.Item.Text) != "" {
			last = ev.Item.Text
		}
	}
	return last
}
