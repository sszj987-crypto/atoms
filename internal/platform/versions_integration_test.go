package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type fixtureVersionRuntime struct {
	failVerify   atomic.Bool
	failPrevious atomic.Bool
	running      atomic.Bool
	entered      chan struct{}
	release      chan struct{}
}

func (v *fixtureVersionRuntime) Stop(ctx context.Context, p Project) error {
	v.running.Store(false)
	return nil
}
func (v *fixtureVersionRuntime) Running(ctx context.Context, p Project) (bool, error) {
	return v.running.Load(), nil
}
func (v *fixtureVersionRuntime) Verify(ctx context.Context, p Project) error {
	if v.entered != nil {
		select {
		case v.entered <- struct{}{}:
		default:
		}
		select {
		case <-v.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if v.failVerify.Load() {
		return errors.New("fixture build failed")
	}
	v.running.Store(true)
	return nil
}
func (v *fixtureVersionRuntime) StartPrevious(ctx context.Context, p Project) error {
	if v.failPrevious.Load() {
		return errors.New("fixture unavailable")
	}
	v.running.Store(true)
	return nil
}
func (v *fixtureVersionRuntime) Capture(ctx context.Context, p Project) []byte { return nil }

func TestVersionsAPIIntegration(t *testing.T) {
	dsn := os.Getenv("ATOMS_FILES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set a dedicated disposable ATOMS_FILES_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	db, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	owner, other := User{ID: newID(), Email: newID() + "@example.test"}, User{ID: newID(), Email: newID() + "@example.test"}
	for _, u := range []User{owner, other} {
		if _, e = db.Exec(ctx, `INSERT INTO users(id,email,password_hash) VALUES($1,$2,'fixture')`, u.ID, u.Email); e != nil {
			t.Fatal(e)
		}
	}
	defer db.Exec(ctx, `DELETE FROM users WHERE id=$1 OR id=$2`, owner.ID, other.ID)
	s := newProjectService(db, Config{}, nil)
	defer s.shutdownRestores(ctx)
	runtime := &fixtureVersionRuntime{}
	runtime.running.Store(true)
	s.versionRuntime = runtime
	auth := newAuthService(db, bytes.Repeat([]byte{1}, 32), 18000, 5)
	app := &App{db: db, auth: auth, projects: s, chat: newChatService(s), cfg: Config{WebDir: t.TempDir()}}
	router := app.Router()
	newProject := func() Project {
		p := versionFixture(t)
		if _, e = db.Exec(ctx, `INSERT INTO projects(id,user_id,name,workspace_path,codex_state_path,last_accessed_at,db_schema,db_username,db_password_ciphertext) VALUES($1,$2,$3,$4,$5,now(),'fixture','fixture','fixture')`, p.ID, owner.ID, p.Name, p.WorkspacePath, p.CodexStatePath); e != nil {
			t.Fatal(e)
		}
		return p
	}
	save := func(p Project, description string) string {
		t.Helper()
		tx, e := db.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		if e = projectLock(ctx, tx, p.ID); e != nil {
			t.Fatal(e)
		}
		id, e := s.saveVersion(ctx, tx, p, description, nil, nil)
		if e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		return id
	}
	request := func(p Project, method, suffix string, user *User, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://localhost/api/project/"+p.ID+suffix, bytes.NewReader(raw))
		if user != nil {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.signSession(user.ID)})
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	assertError := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || !strings.Contains(w.Body.String(), `"error":"`+code+`"`) {
			t.Fatalf("got %d %s, expected %d %s", w.Code, w.Body.String(), status, code)
		}
	}
	wait := func(id string, status string) restoreOperation {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			op, e := scanRestore(db.QueryRow(ctx, `SELECT `+restoreColumns+` FROM project_restores WHERE id=$1`, id))
			if e == nil && op.Status == status {
				return op
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("restore did not reach %s", status)
		return restoreOperation{}
	}
	submit := func(p Project, id string, revision int64, nonce string) string {
		t.Helper()
		w := request(p, "POST", "/restore", &owner, map[string]any{"version_id": id, "expected_revision": revision, "request_id": nonce})
		if w.Code != 202 {
			t.Fatalf("restore submit: %d %s", w.Code, w.Body.String())
		}
		var response struct {
			ID string `json:"operation_id"`
		}
		json.Unmarshal(w.Body.Bytes(), &response)
		return response.ID
	}

	t.Run("ownership validation and stale requests", func(t *testing.T) {
		p := newProject()
		old := save(p, "旧版本")
		save(p, "新版本")
		body := map[string]any{"version_id": old, "expected_revision": 2, "request_id": newID()}
		for _, endpoint := range []struct{ method, path string }{{"GET", "/versions"}, {"GET", "/versions/" + old + "/thumbnail"}, {"POST", "/restore"}, {"GET", "/restore/" + newID()}} {
			assertError(request(p, endpoint.method, endpoint.path, nil, body), 401, "AUTH_REQUIRED")
			assertError(request(p, endpoint.method, endpoint.path, &other, body), 404, "PROJECT_NOT_FOUND")
		}
		body["expected_revision"] = 1
		assertError(request(p, "POST", "/restore", &owner, body), 409, "SOURCE_REVISION_CHANGED")
		body["expected_revision"] = 2
		body["version_id"] = newID()
		assertError(request(p, "POST", "/restore", &owner, body), 404, "VERSION_NOT_FOUND")
		w := request(p, "GET", "/versions", &owner, nil)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(w.Body.String(), p.WorkspacePath) {
			t.Fatalf("versions response: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("restore, busy operations, idempotency and session isolation", func(t *testing.T) {
		p := newProject()
		old := save(p, "旧版页面")
		writeFixtureSource(t, p, "app/page.tsx", "new page")
		writeFixtureSource(t, p, "new.ts", "new source")
		latest := save(p, "新版页面")
		os.WriteFile(filepath.Join(p.CodexStatePath, "old.jsonl"), []byte("old session"), 0600)
		runtime.entered = make(chan struct{}, 1)
		runtime.release = make(chan struct{})
		nonce := newID()
		opID := submit(p, old, 2, nonce)
		select {
		case <-runtime.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("restore did not start")
		}
		if repeat := submit(p, old, 2, nonce); repeat != opID {
			t.Fatal("duplicate request was not idempotent")
		}
		assertError(request(p, "POST", "/restore", &owner, map[string]any{"version_id": latest, "expected_revision": 2, "request_id": nonce}), 409, "RESTORE_REQUEST_CONFLICT")
		for _, endpoint := range []struct{ method, path, code string }{{"POST", "/messages", "AGENT_RUN_IN_PROGRESS"}, {"DELETE", "", "RUN_IN_PROGRESS"}, {"POST", "/runtime/restart", "RUN_IN_PROGRESS"}, {"POST", "/deploy", "RUN_IN_PROGRESS"}, {"GET", "/export", "RUN_IN_PROGRESS"}, {"GET", "/file/download?path=app/page.tsx", "RUN_IN_PROGRESS"}, {"GET", "/files", "PROJECT_RESTORING"}, {"GET", "/file?path=app/page.tsx", "PROJECT_RESTORING"}} {
			assertError(request(p, endpoint.method, endpoint.path, &owner, map[string]string{"content": "new task"}), 409, endpoint.code)
		}
		close(runtime.release)
		wait(opID, "COMPLETED")
		s.restoreWorkers.Wait()
		runtime.entered = nil
		runtime.release = nil
		raw, e := os.ReadFile(filepath.Join(p.WorkspacePath, "app/page.tsx"))
		if e != nil || !strings.Contains(string(raw), "旧版本") {
			t.Fatalf("source not restored: %v", e)
		}
		if _, e = os.Stat(filepath.Join(p.WorkspacePath, "new.ts")); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("new source not removed")
		}
		raw, _ = os.ReadFile(filepath.Join(p.WorkspacePath, ".env.local"))
		if string(raw) != "PRIVATE=keep-current" {
			t.Fatal("private file lost")
		}
		if hasSession(p.CodexStatePath) {
			t.Fatal("old session still active")
		}
		if _, e = os.Stat(filepath.Join(filepath.Dir(p.WorkspacePath), "versions/sessions", opID, "old.jsonl")); e != nil {
			t.Fatal("old session not archived")
		}
		var current string
		var revision int64
		db.QueryRow(ctx, `SELECT current_version_id,source_revision FROM projects WHERE id=$1`, p.ID).Scan(&current, &revision)
		if current != old || revision != 3 {
			t.Fatalf("wrong current metadata %s %d", current, revision)
		}
		opID = submit(p, latest, revision, newID())
		wait(opID, "COMPLETED")
		s.restoreWorkers.Wait()
		raw, _ = os.ReadFile(filepath.Join(p.WorkspacePath, "app/page.tsx"))
		if string(raw) != "new page" {
			t.Fatal("switching back failed")
		}
	})
	t.Run("failed verification restores original workspace", func(t *testing.T) {
		p := newProject()
		old := save(p, "旧版")
		writeFixtureSource(t, p, "app/page.tsx", "current page")
		current := save(p, "当前版")
		runtime.failVerify.Store(true)
		opID := submit(p, old, 2, newID())
		wait(opID, "FAILED")
		s.restoreWorkers.Wait()
		runtime.failVerify.Store(false)
		raw, _ := os.ReadFile(filepath.Join(p.WorkspacePath, "app/page.tsx"))
		if string(raw) != "current page" {
			t.Fatal("original not recovered")
		}
		var id string
		db.QueryRow(ctx, `SELECT current_version_id FROM projects WHERE id=$1`, p.ID).Scan(&id)
		if id != current {
			t.Fatal("failed restore updated current version")
		}
	})
	t.Run("retention and current protection", func(t *testing.T) {
		p := newProject()
		var oldest string
		for i := 0; i < 12; i++ {
			id := save(p, "保存版本")
			if i == 0 {
				oldest = id
			}
		}
		if e = s.cleanVersionStore(ctx, p); e != nil {
			t.Fatal(e)
		}
		var count int
		db.QueryRow(ctx, `SELECT count(*) FROM project_versions WHERE project_id=$1`, p.ID).Scan(&count)
		if count != 10 {
			t.Fatalf("retained %d", count)
		}
		if _, e = os.Stat(filepath.Join(filepath.Dir(p.WorkspacePath), "versions", oldest)); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("pruned snapshot retained")
		}
	})
	t.Run("completion saves source and restored parent atomically", func(t *testing.T) {
		p := newProject()
		old := save(p, "初始状态")
		writeFixtureSource(t, p, "app/page.tsx", "verified new page")
		runID := newID()
		if _, e = db.Exec(ctx, `INSERT INTO agent_runs(id,project_id,status) VALUES($1,$2,'VERIFYING')`, runID, p.ID); e != nil {
			t.Fatal(e)
		}
		app.chat.complete(runID, p, "已完成", "新增首页😀带完整描述")
		var parent, current, status, description string
		if e = db.QueryRow(ctx, `SELECT v.parent_version_id,p.current_version_id,r.status,v.description FROM project_versions v JOIN projects p ON p.id=v.project_id JOIN agent_runs r ON r.id=v.run_id WHERE v.run_id=$1`, runID).Scan(&parent, &current, &status, &description); e != nil {
			t.Fatal(e)
		}
		if parent != old || status != "COMPLETED" || description != "新增首页😀带完整描述" {
			t.Fatal("completion/version were not committed together")
		}
		op := submit(p, old, 2, newID())
		wait(op, "COMPLETED")
		s.restoreWorkers.Wait()
		writeFixtureSource(t, p, "app/page.tsx", "branched page")
		runID = newID()
		db.Exec(ctx, `INSERT INTO agent_runs(id,project_id,status) VALUES($1,$2,'VERIFYING')`, runID, p.ID)
		app.chat.complete(runID, p, "已完成", "从恢复版继续修改")
		if e = db.QueryRow(ctx, `SELECT parent_version_id FROM project_versions WHERE run_id=$1`, runID).Scan(&parent); e != nil || parent != old {
			t.Fatal("new task did not branch from restored version")
		}
		var count int
		db.QueryRow(ctx, `SELECT count(*) FROM messages WHERE project_id=$1`, p.ID).Scan(&count)
		if count != 2 {
			t.Fatal("visible chat changed during restore")
		}
	})
	t.Run("cancelled and failed snapshots do not claim completion", func(t *testing.T) {
		p := newProject()
		save(p, "基线")
		for _, status := range []string{"CANCELLED", "FAILED", "CANCELLING"} {
			runID := newID()
			db.Exec(ctx, `INSERT INTO agent_runs(id,project_id,status) VALUES($1,$2,$3)`, runID, p.ID, status)
			app.chat.complete(runID, p, "should not save", "cancelled")
			var count int
			db.QueryRow(ctx, `SELECT count(*) FROM project_versions WHERE run_id=$1`, runID).Scan(&count)
			if count != 0 {
				t.Fatal("terminal/cancelling run saved a version")
			}
			if status == "CANCELLING" {
				db.Exec(ctx, `UPDATE agent_runs SET status='CANCELLED' WHERE id=$1`, runID)
			}
		}
		runID := newID()
		db.Exec(ctx, `INSERT INTO agent_runs(id,project_id,status) VALUES($1,$2,'VERIFYING')`, runID, p.ID)
		oversized, e := os.Create(filepath.Join(p.WorkspacePath, "oversized.bin"))
		if e != nil {
			t.Fatal(e)
		}
		if e = oversized.Truncate(maxSourceExportBytes + 1); e != nil {
			t.Fatal(e)
		}
		oversized.Close()
		app.chat.complete(runID, p, "should fail", "too large")
		var status string
		var count int
		db.QueryRow(ctx, `SELECT status FROM agent_runs WHERE id=$1`, runID).Scan(&status)
		db.QueryRow(ctx, `SELECT count(*) FROM project_versions WHERE project_id=$1`, p.ID).Scan(&count)
		if status != "FAILED" || count != 1 {
			t.Fatalf("snapshot failure claimed completion: %s %d", status, count)
		}
	})
	t.Run("restore waits for export project lock and rechecks busy", func(t *testing.T) {
		p := newProject()
		old := save(p, "基线")
		save(p, "新版")
		tx, e := db.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		if e = projectLock(ctx, tx, p.ID); e != nil {
			t.Fatal(e)
		}
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			result <- request(p, "POST", "/restore", &owner, map[string]any{"version_id": old, "expected_revision": 2, "request_id": newID()})
		}()
		select {
		case <-result:
			t.Fatal("restore bypassed export lock")
		case <-time.After(50 * time.Millisecond):
		}
		if _, e = tx.Exec(ctx, `INSERT INTO agent_runs(id,project_id,status) VALUES($1,$2,'RUNNING')`, newID(), p.ID); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		select {
		case w := <-result:
			assertError(w, 409, "PROJECT_BUSY")
		case <-time.After(3 * time.Second):
			t.Fatal("restore never acquired released lock")
		}
	})
	t.Run("cancelling run remains busy", func(t *testing.T) {
		p := newProject()
		old := save(p, "旧版")
		save(p, "新版")
		id := newID()
		db.Exec(ctx, `INSERT INTO agent_runs(id,project_id,status) VALUES($1,$2,'CANCELLING')`, id, p.ID)
		assertError(request(p, "POST", "/restore", &owner, map[string]any{"version_id": old, "expected_revision": 2, "request_id": newID()}), 409, "PROJECT_BUSY")
		assertError(request(p, "GET", "/export", &owner, nil), 409, "RUN_IN_PROGRESS")
	})
	t.Run("cancel cleanup waits for worker before allowing restore", func(t *testing.T) {
		p := newProject()
		old := save(p, "基线")
		save(p, "新版")
		runID := newID()
		if _, e = db.Exec(ctx, `INSERT INTO agent_runs(id,project_id,status) VALUES($1,$2,'RUNNING')`, runID, p.ID); e != nil {
			t.Fatal(e)
		}
		cancelled := make(chan struct{})
		app.chat.mu.Lock()
		app.chat.done[runID] = make(chan struct{})
		app.chat.cancels[runID] = func() { close(cancelled) }
		app.chat.mu.Unlock()
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			r := httptest.NewRequest("POST", "http://localhost/api/project/runs/"+runID+"/cancel", nil)
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.signSession(owner.ID)})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			result <- w
		}()
		select {
		case <-cancelled:
		case <-time.After(3 * time.Second):
			t.Fatal("worker not cancelled")
		}
		assertError(request(p, "POST", "/restore", &owner, map[string]any{"version_id": old, "expected_revision": 2, "request_id": newID()}), 409, "PROJECT_BUSY")
		assertError(request(p, "GET", "/export", &owner, nil), 409, "RUN_IN_PROGRESS")
		select {
		case <-result:
			t.Fatal("cancel completed before worker stopped")
		default:
		}
		app.chat.unregisterRun(runID)
		select {
		case w := <-result:
			if w.Code != 200 {
				t.Fatalf("cancel cleanup: %d %s", w.Code, w.Body.String())
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancel cleanup did not finish")
		}
		op := submit(p, old, 2, newID())
		wait(op, "COMPLETED")
		s.restoreWorkers.Wait()
	})

	t.Run("interrupted directory phases and repeat recovery", func(t *testing.T) {
		for _, phase := range []string{"PREPARING", "STOPPING", "BACKING_UP", "SWAPPING", "VERIFYING", "SESSION_HANDOFF", "COMMITTING"} {
			t.Run(phase, func(t *testing.T) {
				p := newProject()
				old := save(p, "历史版")
				writeFixtureSource(t, p, "app/page.tsx", "original live page")
				original := save(p, "原版")
				manifest, e := sourceManifest(ctx, p.WorkspacePath)
				if e != nil {
					t.Fatal(e)
				}
				id := newID()
				job := filepath.Join(filepath.Dir(p.WorkspacePath), "versions/restore-"+id)
				os.MkdirAll(filepath.Join(job, "next"), 0750)
				if phase == "BACKING_UP" || phase == "SWAPPING" || phase == "VERIFYING" || phase == "SESSION_HANDOFF" || phase == "COMMITTING" {
					if e = os.Rename(p.WorkspacePath, filepath.Join(job, "previous")); e != nil {
						t.Fatal(e)
					}
				}
				if phase == "SWAPPING" || phase == "VERIFYING" || phase == "SESSION_HANDOFF" || phase == "COMMITTING" {
					os.Rename(filepath.Join(job, "next"), p.WorkspacePath)
					writeFixtureSource(t, p, "app/page.tsx", "rejected target")
				}
				if phase == "SESSION_HANDOFF" || phase == "COMMITTING" {
					os.MkdirAll(filepath.Join(filepath.Dir(p.WorkspacePath), "versions/sessions"), 0700)
					os.WriteFile(filepath.Join(p.CodexStatePath, "old.jsonl"), []byte("old"), 0600)
					os.Rename(p.CodexStatePath, filepath.Join(filepath.Dir(p.WorkspacePath), "versions/sessions", id))
					os.Mkdir(p.CodexStatePath, 0750)
				}
				if _, e = db.Exec(ctx, `INSERT INTO project_restores(id,project_id,version_id,request_id,expected_revision,status,phase,previous_runtime_running,previous_source_hash) VALUES($1,$2,$3,$4,2,'RUNNING',$5,true,$6)`, id, p.ID, old, newID(), phase, manifest.Hash); e != nil {
					t.Fatal(e)
				}
				if phase == "COMMITTING" {
					runtime.failPrevious.Store(true)
				}
				e = s.recoverRestore(context.WithValue(ctx, restoreContextKey{}, p.ID), p, id, "测试中断已还原")
				if phase == "COMMITTING" {
					if e == nil {
						t.Fatal("recovery failure not protected")
					}
					wait(id, "BLOCKED")
					runtime.failPrevious.Store(false)
					e = s.recoverRestore(context.WithValue(ctx, restoreContextKey{}, p.ID), p, id, "重试还原")
				}
				if e != nil {
					t.Fatal(e)
				}
				wait(id, "FAILED")
				raw, e := os.ReadFile(filepath.Join(p.WorkspacePath, "app/page.tsx"))
				if e != nil || string(raw) != "original live page" {
					t.Fatalf("phase %s not recovered: %v", phase, e)
				}
				var current string
				db.QueryRow(ctx, `SELECT current_version_id FROM projects WHERE id=$1`, p.ID).Scan(&current)
				if current != original {
					t.Fatal("current version changed on recovery")
				}
			})
		}
	})
}
