package platform

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type blockedSourceResponse struct {
	*httptest.ResponseRecorder
	ready   chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockedSourceResponse) Write(raw []byte) (int, error) {
	w.once.Do(func() { close(w.ready); <-w.release })
	return w.ResponseRecorder.Write(raw)
}

// These tests only insert/delete their own fixtures, but must run against a
// disposable database because migrate creates the platform schema.
func TestSourceAPIIntegration(t *testing.T) {
	dsn := os.Getenv("ATOMS_FILES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set ATOMS_FILES_TEST_DATABASE_URL to a dedicated test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	owner, other := User{ID: newID(), Email: "files-" + newID() + "@example.test"}, User{ID: newID(), Email: "files-" + newID() + "@example.test"}
	for _, user := range []User{owner, other} {
		if _, err := db.Exec(ctx, `INSERT INTO users(id,email,password_hash) VALUES($1,$2,'fixture')`, user.ID, user.Email); err != nil {
			t.Fatal(err)
		}
	}
	defer db.Exec(context.Background(), `DELETE FROM users WHERE id=$1 OR id=$2`, owner.ID, other.ID)
	dir, _ := sourceFixture(t, map[string][]byte{
		"app/page.tsx": []byte("export default function Page() { return '你好' }"), "package.json": []byte("{}"), "pnpm-lock.yaml": []byte("lockfileVersion: 9"), "public/中文 图片.png": {0, 1, 2}, ".env": []byte("secret"), "node_modules/a/index.js": []byte("dependency"),
	})
	p := Project{ID: newID(), Name: "测试项目", WorkspacePath: dir}
	if _, err := db.Exec(ctx, `INSERT INTO projects(id,user_id,name,workspace_path,codex_state_path,last_accessed_at,db_schema,db_username,db_password_ciphertext) VALUES($1,$2,$3,$4,$4,now(),'fixture','fixture','fixture')`, p.ID, owner.ID, p.Name, p.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	service := newProjectService(db, Config{}, nil)
	auth := newAuthService(db, bytes.Repeat([]byte{1}, 32), 18000, 5)
	app := &App{db: db, auth: auth, projects: service, chat: newChatService(service), cfg: Config{WebDir: t.TempDir()}}
	router := app.Router()
	base := "/api/project/" + p.ID
	request := func(suffix string, user *User) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "http://localhost"+base+suffix, nil)
		if user != nil {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.signSession(user.ID)})
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		return response
	}
	assertError := func(response *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if response.Code != status || !strings.Contains(response.Body.String(), `"error":"`+code+`"`) {
			t.Fatalf("response = %d %s, want %d %s", response.Code, response.Body.String(), status, code)
		}
		if strings.Contains(response.Body.String(), dir) {
			t.Fatal("response leaked absolute workspace path")
		}
	}
	for _, suffix := range []string{"/files", "/file?path=app/page.tsx", "/file/download?path=app/page.tsx", "/export"} {
		t.Run("auth "+suffix, func(t *testing.T) {
			assertError(request(suffix, nil), http.StatusUnauthorized, "AUTH_REQUIRED")
			assertError(request(suffix, &other), http.StatusNotFound, "PROJECT_NOT_FOUND")
		})
	}
	t.Run("list and read without runtime", func(t *testing.T) {
		response := request("/files", &owner)
		var result struct {
			Files     []sourceEntry `json:"files"`
			RunActive bool          `json:"run_active"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.RunActive || len(result.Files) != 6 {
			t.Fatalf("list = %d %s", response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(response.Body.String(), dir) {
			t.Fatal("missing private response or leaked root path")
		}
		response = request("/file?path=app/page.tsx", &owner)
		var file sourceFile
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &file) != nil || !file.Previewable || !strings.Contains(file.Content, "你好") {
			t.Fatalf("read = %d %s", response.Code, response.Body.String())
		}
		response = request("/file?path="+url.QueryEscape("public/中文 图片.png"), &owner)
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &file) != nil || file.Previewable || file.Reason != "binary" {
			t.Fatalf("binary = %d %s", response.Code, response.Body.String())
		}
		for _, suffix := range []string{"/file?path=missing.ts", "/file/download?path=missing.ts"} {
			assertError(request(suffix, &owner), http.StatusNotFound, "FILE_NOT_FOUND")
		}
		for _, name := range []string{"../secret", ".env", "node_modules/a/index.js", "/etc/passwd"} {
			for _, suffix := range []string{"/file?path=", "/file/download?path="} {
				assertError(request(suffix+url.QueryEscape(name), &owner), http.StatusBadRequest, "INVALID_FILE_PATH")
			}
		}
	})
	t.Run("downloads and temp cleanup", func(t *testing.T) {
		tempDir := t.TempDir()
		t.Setenv("TMPDIR", tempDir)
		response := request("/file/download?path="+url.QueryEscape("public/中文 图片.png"), &owner)
		_, params, err := mime.ParseMediaType(response.Header().Get("Content-Disposition"))
		if response.Code != http.StatusOK || err != nil || params["filename"] != "中文 图片.png" || !bytes.Equal(response.Body.Bytes(), []byte{0, 1, 2}) || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Content-Type") != "application/octet-stream" {
			t.Fatalf("download metadata/body mismatch: status=%d headers=%v", response.Code, response.Header())
		}
		response = request("/export", &owner)
		if response.Code != http.StatusOK {
			t.Fatalf("export = %d %s", response.Code, response.Body.String())
		}
		archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		found := make(map[string]bool)
		for _, file := range archive.File {
			found[file.Name] = true
			if !sourcePathAllowed(strings.TrimSuffix(file.Name, "/")) {
				t.Fatalf("excluded file in export: %s", file.Name)
			}
		}
		if !found["app/page.tsx"] || !found["package.json"] || !found["pnpm-lock.yaml"] {
			t.Fatal("ZIP missing source or dependency files")
		}
		children, err := os.ReadDir(tempDir)
		if err != nil || len(children) != 0 {
			t.Fatalf("temporary snapshots not cleaned up: %v %v", children, err)
		}
	})
	t.Run("active task permits reads and blocks downloads", func(t *testing.T) {
		runID := newID()
		if _, err := db.Exec(ctx, `INSERT INTO agent_runs(id,project_id,status) VALUES($1,$2,'VERIFYING')`, runID, p.ID); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(ctx, `DELETE FROM agent_runs WHERE id=$1`, runID)
		if response := request("/files", &owner); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"run_active":true`) {
			t.Fatalf("running list = %d %s", response.Code, response.Body.String())
		}
		if response := request("/file?path=app/page.tsx", &owner); response.Code != http.StatusOK {
			t.Fatalf("read rejected during active work: %d", response.Code)
		}
		assertError(request("/file/download?path=app/page.tsx", &owner), http.StatusConflict, "RUN_IN_PROGRESS")
		assertError(request("/export", &owner), http.StatusConflict, "RUN_IN_PROGRESS")
	})
	t.Run("release project lock before transfer and keep snapshot immutable", func(t *testing.T) {
		response := &blockedSourceResponse{ResponseRecorder: httptest.NewRecorder(), ready: make(chan struct{}), release: make(chan struct{})}
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(response.release) })
		done := make(chan struct{})
		r := httptest.NewRequest(http.MethodGet, "http://localhost"+base+"/export", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.signSession(owner.ID)})
		go func() { router.ServeHTTP(response, r); close(done) }()
		select {
		case <-response.ready:
		case <-time.After(5 * time.Second):
			t.Fatal("export did not prepare a response")
		}
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		var available bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext($1))`, p.ID).Scan(&available); err != nil || !available {
			t.Fatalf("export held the project lock during client transfer: available=%v err=%v", available, err)
		}
		original, err := os.ReadFile(filepath.Join(dir, "app/page.tsx"))
		if err != nil {
			t.Fatal(err)
		}
		defer os.WriteFile(filepath.Join(dir, "app/page.tsx"), original, 0640)
		if err := os.WriteFile(filepath.Join(dir, "app/page.tsx"), []byte("changed after snapshot"), 0640); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		releaseOnce.Do(func() { close(response.release) })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("export transfer did not finish")
		}
		archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range archive.File {
			if file.Name != "app/page.tsx" {
				continue
			}
			reader, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(reader)
			reader.Close()
			if err != nil || !bytes.Equal(raw, original) {
				t.Fatalf("export changed after releasing the lock: %q %v", raw, err)
			}
			return
		}
		t.Fatal("snapshot missing original source")
	})
	t.Run("recheck active task after waiting on project lock", func(t *testing.T) {
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, p.ID); err != nil {
			t.Fatal(err)
		}
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() { result <- request("/export", &owner) }()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var waiting bool
			err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory')`).Scan(&waiting)
			if err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("export did not acquire the shared project advisory lock")
			}
			time.Sleep(10 * time.Millisecond)
		}
		runID := newID()
		if _, err := tx.Exec(ctx, `INSERT INTO agent_runs(id,project_id,status) VALUES($1,$2,'PENDING')`, runID, p.ID); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(ctx, `DELETE FROM agent_runs WHERE id=$1`, runID)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case response := <-result:
			assertError(response, http.StatusConflict, "RUN_IN_PROGRESS")
		case <-time.After(5 * time.Second):
			t.Fatal("export did not finish after releasing the lock")
		}
	})
	t.Run("limit failures return JSON and clean temporary snapshot", func(t *testing.T) {
		tempDir := t.TempDir()
		t.Setenv("TMPDIR", tempDir)
		large, err := os.Create(filepath.Join(dir, "large.bin"))
		if err != nil {
			t.Fatal(err)
		}
		if err := large.Truncate(maxSourceExportBytes + 1); err != nil {
			t.Fatal(err)
		}
		large.Close()
		assertError(request("/export", &owner), http.StatusRequestEntityTooLarge, "SOURCE_LIMIT_EXCEEDED")
		children, err := os.ReadDir(tempDir)
		if err != nil || len(children) != 0 {
			t.Fatalf("failed snapshot not cleaned up: %v %v", children, err)
		}
	})
}

func TestZipSourceFileCountLimit(t *testing.T) {
	dir, root := sourceFixture(t, nil)
	for index := 0; index <= maxSourceExportFiles; index++ {
		file, err := os.CreateTemp(dir, "source-*.ts")
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	if err := zipSource(context.Background(), root, io.Discard); err != errSourceLimit {
		t.Fatalf("too many files not rejected: %v", err)
	}
}
