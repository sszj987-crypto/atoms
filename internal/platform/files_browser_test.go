package platform

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Opt-in manual browser harness: real platform authentication, file APIs,
// database and frontend, but a labelled fixture instead of Docker/paid models.
// It never connects to or recreates the user's project runtimes.
func TestSourceBrowserFixture(t *testing.T) {
	if os.Getenv("ATOMS_FILES_BROWSER_FIXTURE") != "1" {
		t.Skip("set ATOMS_FILES_BROWSER_FIXTURE=1 and a disposable test database to serve browser fixtures")
	}
	dsn := os.Getenv("ATOMS_FILES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("ATOMS_FILES_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	user := User{ID: newID(), Email: "files-browser@example.test"}
	password, err := hashPassword("Files-browser-fixture-123!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO users(id,email,password_hash) VALUES($1,$2,$3)`, user.ID, user.Email, password); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, `DELETE FROM users WHERE id=$1`, user.ID)
	dir, _ := sourceFixture(t, map[string][]byte{
		"app/page.tsx": []byte("export default function Page() {\n  return <h1>Hello fixture</h1>;\n}\n"),
		"package.json": []byte("{\n  \"name\": \"files-fixture\"\n}\n"), "pnpm-lock.yaml": []byte("lockfileVersion: 9\n"), ".env.example": []byte("DATABASE_URL=\n"), ".env.local": []byte("FIXTURE_SECRET=not-a-real-key"), "node_modules/a/index.js": []byte("excluded"), "public/中文 图片.png": {0, 1, 2}, "large.txt": bytes.Repeat([]byte("a"), maxSourcePreviewBytes+1), "empty.ts": {},
	})
	p := Project{ID: newID(), Name: "文件区测试项目", WorkspacePath: dir}
	if _, err := db.Exec(ctx, `INSERT INTO projects(id,user_id,name,workspace_path,codex_state_path,last_accessed_at,db_schema,db_username,db_password_ciphertext) VALUES($1,$2,$3,$4,$4,now(),'fixture','fixture','fixture')`, p.ID, user.ID, p.Name, dir); err != nil {
		t.Fatal(err)
	}
	webDir, err := filepath.Abs("../../web/dist")
	if err != nil {
		t.Fatal(err)
	}
	service := newProjectService(db, Config{}, nil)
	auth := newAuthService(db, bytes.Repeat([]byte{1}, 32), 18000, 5)
	app := &App{db: db, auth: auth, projects: service, chat: newChatService(service), cfg: Config{WebDir: webDir}}
	router := app.Router()
	stop := make(chan struct{})
	var stopOnce sync.Once
	var failFiles atomic.Bool
	var work atomic.Bool
	base := "/api/project/" + p.ID
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/__fixture") || r.URL.Path == base+"/preview-access" || r.URL.Path == base+"/runtime/status" {
			auth.requireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case base + "/preview-access":
					writeJSON(w, 200, map[string]string{"url": "http://files-fixture.localhost:19082"})
				case base + "/runtime/status":
					writeJSON(w, 200, map[string]bool{"exists": true, "running": true})
				case "/__fixture/stop":
					stopOnce.Do(func() { close(stop) })
				case "/__fixture/fail":
					failFiles.Store(true)
					http.Redirect(w, r, "/__fixture", http.StatusSeeOther)
				case "/__fixture/recover":
					failFiles.Store(false)
					http.Redirect(w, r, "/__fixture", http.StatusSeeOther)
				case "/__fixture/work":
					if !work.CompareAndSwap(false, true) {
						http.Error(w, "fixture work active", 409)
						return
					}
					runID := newID()
					if _, err := db.Exec(ctx, `INSERT INTO agent_runs(id,project_id,status) VALUES($1,$2,'RUNNING')`, runID, p.ID); err != nil {
						work.Store(false)
						http.Error(w, "fixture run failed", 500)
						return
					}
					go func() {
						defer work.Store(false)
						select {
						case <-stop:
							return
						case <-time.After(12 * time.Second):
						}
						os.WriteFile(filepath.Join(dir, "app/page.tsx"), []byte("export default function Page() {\n  return <h1>Updated fixture</h1>;\n}\n"), 0640)
						db.Exec(ctx, `UPDATE agent_runs SET status='COMPLETED',finished_at=now() WHERE id=$1`, runID)
					}()
					http.Redirect(w, r, "/__fixture", http.StatusSeeOther)
				default:
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					fmt.Fprintf(w, `<h1>Browser test controls (fixture only)</h1><p><a href="/projects/%s">Open fixture project</a></p><form method="post" action="/__fixture/work"><button>Simulate development</button></form><form method="post" action="/__fixture/fail"><button>Fail file listing</button></form><form method="post" action="/__fixture/recover"><button>Recover file listing</button></form>`, p.ID)
				}
			})).ServeHTTP(w, r)
			return
		}
		if failFiles.Load() && r.URL.Path == base+"/files" {
			auth.requireUser(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { apiError(w, 500, "FILE_READ_FAILED") })).ServeHTTP(w, r)
			return
		}
		router.ServeHTTP(w, r)
	})
	preview := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		raw, _ := os.ReadFile(filepath.Join(dir, "app/page.tsx"))
		fmt.Fprintf(w, `<h1>Preview fixture (not a generated runtime)</h1><label>Preview state<input aria-label="Preview state" value="state retained"></label><pre>%s</pre>`, html.EscapeString(string(raw)))
	})}
	server := &http.Server{Handler: handler}
	listener, err := net.Listen("tcp", "127.0.0.1:19081")
	if err != nil {
		t.Fatal(err)
	}
	previewListener, err := net.Listen("tcp", "127.0.0.1:19082")
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	defer server.Close()
	defer preview.Close()
	go server.Serve(listener)
	go preview.Serve(previewListener)
	// A separate localhost hostname avoids replacing the normal app's cookie.
	t.Logf("Fixture: http://files-fixture.localhost:19081/projects/%s ; login files-browser@example.test / Files-browser-fixture-123!", p.ID)
	select {
	case <-stop:
	case <-time.After(15 * time.Minute):
	}
}
