package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type chatService struct{ projects *projectService }

func newChatService(p *projectService) *chatService { return &chatService{projects: p} }
func (s *chatService) messages(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(currentUserKey{}).(User)
	p, e := s.projects.current(r.Context(), u.ID)
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
	if decodeJSON(r, &in) != nil || in.Content == "" {
		apiError(w, 400, "INVALID_MESSAGE")
		return
	}
	u := r.Context().Value(currentUserKey{}).(User)
	p, e := s.projects.current(r.Context(), u.ID)
	if e != nil {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return
	}
	var active bool
	e = s.projects.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE project_id=$1 AND status IN ('PENDING','RUNNING','VERIFYING','REPAIRING'))`, p.ID).Scan(&active)
	if e != nil {
		apiError(w, 500, "RUN_READ_FAILED")
		return
	}
	if active {
		apiError(w, 409, "AGENT_RUN_IN_PROGRESS")
		return
	}
	mid, rid := newID(), newID()
	tx, e := s.projects.db.Begin(r.Context())
	if e != nil {
		apiError(w, 500, "RUN_CREATE_FAILED")
		return
	}
	defer tx.Rollback(r.Context())
	_, e = tx.Exec(r.Context(), `INSERT INTO messages(id,project_id,role,content,selected_ui) VALUES($1,$2,'user',$3,$4)`, mid, p.ID, in.Content, in.SelectedUI)
	if e == nil {
		_, e = tx.Exec(r.Context(), `INSERT INTO agent_runs(id,project_id,status,user_message_id,started_at) VALUES($1,$2,'PENDING',$3,now())`, rid, p.ID, mid)
	}
	if e != nil || tx.Commit(r.Context()) != nil {
		apiError(w, 500, "RUN_CREATE_FAILED")
		return
	}
	go s.run(context.Background(), p, u.ID, rid, in.Content, in.SelectedUI)
	writeJSON(w, 202, map[string]string{"run_id": rid})
}
func (s *chatService) run(ctx context.Context, p Project, userID, runID, prompt string, selected any) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	_, _ = s.projects.db.Exec(ctx, `UPDATE agent_runs SET status='RUNNING' WHERE id=$1`, runID)
	var base, model string
	var sealed []byte
	err := s.projects.db.QueryRow(ctx, `SELECT base_url,api_key_ciphertext,model FROM llm_configs WHERE user_id=$1`, userID).Scan(&base, &sealed, &model)
	if err != nil {
		s.fail(ctx, runID, "Model configuration is required.")
		return
	}
	key, err := decrypt(s.projects.cfg.MasterKey, sealed)
	if err != nil {
		s.fail(ctx, runID, "Model configuration could not be read.")
		return
	}
	if err = s.projects.ensureRuntime(ctx, p); err != nil {
		s.fail(ctx, runID, "Runtime could not be started.")
		return
	}
	config := fmt.Sprintf("model_provider = \"atoms\"\nmodel = %q\n[model_providers.atoms]\nname = \"Atoms\"\nbase_url = %q\nenv_key = \"OPENAI_API_KEY\"\nwire_api = \"responses\"\n", model, strings.TrimRight(base, "/"))
	if err = os.WriteFile(filepath.Join(p.CodexStatePath, "config.toml"), []byte(config), 0600); err != nil {
		s.fail(ctx, runID, "Session configuration failed.")
		return
	}
	if os.Geteuid() == 0 {
		_ = os.Chown(filepath.Join(p.CodexStatePath, "config.toml"), 1000, 1000)
	}
	task := prompt
	if selected != nil {
		selectedJSON, marshalErr := json.Marshal(selected)
		if marshalErr != nil {
			s.fail(ctx, runID, "Selected UI context could not be encoded.")
			return
		}
		task = "The user selected a rendered UI element. Locate its implementation and make the smallest reasonable change.\n\nSelected UI context:\n" + string(selectedJSON) + "\n\nUser request:\n" + prompt
	}
	cmd := []string{"codex", "exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-C", "/workspace"}
	if hasSession(p.CodexStatePath) {
		cmd = append(cmd, "resume", "--last")
	}
	cmd = append(cmd, task)
	out, err := s.projects.docker.exec(ctx, runtimeName(p.ID), cmd, []string{"CODEX_HOME=/codex", "OPENAI_API_KEY=" + key})
	// Execution logs are retained for diagnosis but must never persist the provider key.
	redacted := []byte(strings.ReplaceAll(string(out), key, "[REDACTED]"))
	_ = os.WriteFile(filepath.Join(filepath.Dir(p.WorkspacePath), "logs", runID+".ndjson"), redacted, 0600)
	if err != nil {
		s.fail(ctx, runID, "Application update failed.")
		return
	}
	for repair := 0; repair <= 2; repair++ {
		_, _ = s.projects.db.Exec(ctx, `UPDATE agent_runs SET status='VERIFYING' WHERE id=$1`, runID)
		if verifyErr := s.verify(ctx, p); verifyErr == nil {
			break
		} else if repair == 2 {
			s.fail(ctx, runID, "Verification failed after repair attempts.")
			return
		} else {
			_, _ = s.projects.db.Exec(ctx, `UPDATE agent_runs SET status='REPAIRING' WHERE id=$1`, runID)
			fix := []string{"codex", "exec", "resume", "--last", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "Fix the verification failure: " + verifyErr.Error() + ". Then rerun the required checks."}
			if _, err := s.projects.docker.exec(ctx, runtimeName(p.ID), fix, []string{"CODEX_HOME=/codex", "OPENAI_API_KEY=" + key}); err != nil {
				s.fail(ctx, runID, "Automatic repair failed.")
				return
			}
		}
	}
	summary := extractSummary(out)
	if summary == "" {
		summary = "应用更新完成。"
	}
	_, _ = s.projects.db.Exec(ctx, `INSERT INTO messages(id,project_id,role,content) SELECT $1,$2,'assistant',$3 WHERE EXISTS (SELECT 1 FROM agent_runs WHERE id=$4 AND status <> 'CANCELLED')`, newID(), p.ID, summary, runID)
	_, _ = s.projects.db.Exec(ctx, `UPDATE agent_runs SET status='COMPLETED',finished_at=now() WHERE id=$1 AND status <> 'CANCELLED'`, runID)
}
func (s *chatService) verify(ctx context.Context, p Project) error {
	for _, cmd := range [][]string{{"sh", "-lc", "pnpm typecheck"}, {"sh", "-lc", "pnpm test"}, {"sh", "-lc", "pnpm build"}, {"sh", "-lc", "curl -fsS http://127.0.0.1:3000/ >/dev/null"}} {
		if _, err := s.projects.docker.exec(ctx, runtimeName(p.ID), cmd, nil); err != nil {
			return fmt.Errorf("%s", cmd[2])
		}
	}
	return nil
}
func (s *chatService) fail(ctx context.Context, id, msg string) {
	_, _ = s.projects.db.Exec(ctx, `UPDATE agent_runs SET status='FAILED',error_message=$2,finished_at=now() WHERE id=$1`, id, msg)
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
	for {
		run, exists := s.ownedRun(w, r)
		if !exists {
			return
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
	_, e := s.projects.db.Exec(r.Context(), `UPDATE agent_runs SET status='CANCELLED',finished_at=now() WHERE id=$1 AND status IN ('PENDING','RUNNING','VERIFYING','REPAIRING')`, run.ID)
	if e != nil {
		apiError(w, 500, "RUN_CANCEL_FAILED")
		return
	}
	p, _ := s.projects.current(r.Context(), r.Context().Value(currentUserKey{}).(User).ID)
	go s.projects.docker.exec(context.Background(), runtimeName(p.ID), []string{"sh", "-lc", "pkill -f 'codex exec' || true"}, nil)
	writeJSON(w, 200, map[string]string{"status": "CANCELLED"})
}

type runView struct {
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	ErrorMessage *string `json:"error_message,omitempty"`
	StartedAt    any     `json:"started_at"`
	FinishedAt   any     `json:"finished_at"`
}

func (s *chatService) ownedRun(w http.ResponseWriter, r *http.Request) (runView, bool) {
	u := r.Context().Value(currentUserKey{}).(User)
	p, e := s.projects.current(r.Context(), u.ID)
	if e != nil {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return runView{}, false
	}
	var v runView
	e = s.projects.db.QueryRow(r.Context(), `SELECT id,status,error_message,started_at,finished_at FROM agent_runs WHERE id=$1 AND project_id=$2`, chi.URLParam(r, "id"), p.ID).Scan(&v.ID, &v.Status, &v.ErrorMessage, &v.StartedAt, &v.FinishedAt)
	if e != nil {
		apiError(w, 404, "RUN_NOT_FOUND")
		return runView{}, false
	}
	return v, true
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
