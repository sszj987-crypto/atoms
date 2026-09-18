package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Run inside a disposable control-plane container with a dedicated PostgreSQL
// database, Docker network, and /data volume. Never call NewApp/recover here:
// its normal startup sweep must not see unrelated user containers as orphans.
func TestVersionsRealRuntime(t *testing.T) {
	if os.Getenv("ATOMS_VERSIONS_REAL_RUNTIME") != "1" {
		t.Skip("requires an explicitly isolated Runtime test container")
	}
	volume, network := os.Getenv("ATOMS_VERSIONS_TEST_VOLUME"), os.Getenv("ATOMS_VERSIONS_TEST_NETWORK")
	if !strings.HasPrefix(volume, "atoms-versions-test-") || !strings.HasPrefix(network, "atoms-versions-test-") || os.Getenv("ATOMS_VERSIONS_TEST_DATABASE_URL") == "" {
		t.Fatal("dedicated test volume, network and database are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	db, err := pgxpool.New(ctx, os.Getenv("ATOMS_VERSIONS_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{3}, 32)
	cfg := Config{ProjectRoot: "/data/users", MasterKey: key, SessionKey: key, RuntimeImage: os.Getenv("ATOMS_VERSIONS_TEST_IMAGE"), RuntimeNetwork: network, DataVolumeName: volume, RuntimeCPU: 2, RuntimeMemoryBytes: 2 << 30, RuntimePIDs: 256, DeployPortBase: 19410, DeployPortSpan: 2, WebDir: "/test-web"}
	if cfg.RuntimeImage == "" {
		t.Fatal("explicit test Runtime image is required")
	}
	s := newProjectService(db, cfg, newModelService(db, key))
	defer s.shutdownRestores(context.Background())
	owner := User{ID: newID(), Email: "versions-runtime@example.test"}
	password, err := hashPassword("Versions-runtime-fixture-123!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO users(id,email,password_hash,deploy_port) VALUES($1,$2,$3,19410)`, owner.ID, owner.Email, password); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner.ID)
	p, err := s.createProject(ctx, owner.ID, "独立版本验收项目")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = s.shutdownRestores(context.Background())
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		_ = s.removeRuntime(cleanup, p.ID)
		_, _ = db.Exec(cleanup, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdent(p.DBSchema)))
		_, _ = db.Exec(cleanup, fmt.Sprintf("DROP ROLE IF EXISTS %s", quoteIdent(p.DBUsername)))
	}()
	if _, err = db.Exec(ctx, fmt.Sprintf("CREATE TABLE %s.restore_sentinel(value text); INSERT INTO %s.restore_sentinel VALUES('business-data-kept')", quoteIdent(p.DBSchema), quoteIdent(p.DBSchema))); err != nil {
		t.Fatal(err)
	}
	chat := newChatService(s)
	auth := newAuthService(db, key, 19410, 2)
	app := &App{db: db, auth: auth, model: s.model, projects: s, chat: chat, cfg: cfg}
	router := app.Router()
	if err = s.ensureRuntime(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = (projectVersionRuntime{s}).StartPrevious(ctx, p); err != nil {
		t.Fatal(err)
	}
	var initialHash string
	if err = db.QueryRow(ctx, `SELECT v.source_hash FROM projects p JOIN project_versions v ON v.id=p.current_version_id WHERE p.id=$1`, p.ID).Scan(&initialHash); err != nil {
		t.Fatal(err)
	}
	initial, err := sourceManifest(ctx, p.WorkspacePath)
	if err != nil || initial.Hash != initialHash {
		t.Fatalf("starter startup changed initial source: %v %s != %s", err, initial.Hash, initialHash)
	}
	t.Log("isolated Runtime started, creating verified source versions")
	pagePath := filepath.Join(p.WorkspacePath, "app/page.tsx")
	originalPage, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	makeVersion := func(marker string) (string, string) {
		t.Helper()
		raw := strings.ReplaceAll(string(originalPage), "ATOMS STARTER", marker)
		if err := os.WriteFile(pagePath, []byte(raw), 0644); err != nil {
			t.Fatal(err)
		}
		runID := newID()
		if _, err := db.Exec(ctx, `INSERT INTO agent_runs(id,project_id,status) VALUES($1,$2,'VERIFYING')`, runID, p.ID); err != nil {
			t.Fatal(err)
		}
		if err := chat.verify(ctx, p, runID); err != nil {
			t.Fatal(err)
		}
		chat.complete(runID, p, "已验证修改", "修改首页为 "+marker)
		var versionID, hash, status string
		if err := db.QueryRow(ctx, `SELECT v.id,v.source_hash,r.status FROM project_versions v JOIN agent_runs r ON r.id=v.run_id WHERE v.run_id=$1`, runID).Scan(&versionID, &hash, &status); err != nil {
			var failure *string
			_ = db.QueryRow(ctx, `SELECT status,error_message FROM agent_runs WHERE id=$1`, runID).Scan(&status, &failure)
			t.Fatalf("version not saved: %v; run=%s error=%v", err, status, failure)
		}
		if status != "COMPLETED" {
			t.Fatalf("run status %s", status)
		}
		var hasImage bool
		_ = db.QueryRow(ctx, `SELECT has_thumbnail FROM project_versions WHERE id=$1`, versionID).Scan(&hasImage)
		if !hasImage {
			if os.Getenv("ATOMS_VERSIONS_REQUIRE_SCREENSHOTS") == "1" {
				t.Fatal("fixed homepage screenshot unavailable")
			}
			t.Log("homepage capture unavailable in selected test image; source version still saved")
		} else {
			r := httptest.NewRequest("GET", "/api/project/"+p.ID+"/versions/"+versionID+"/thumbnail", nil)
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.signSession(owner.ID)})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Content-Type") != "image/png" {
				t.Fatal("private thumbnail API failed")
			}
			t.Logf("fixed homepage screenshot and authenticated thumbnail verified for %s", marker)
		}
		return versionID, hash
	}
	old, oldHash := makeVersion("VERSION BEFORE")
	latest, latestHash := makeVersion("VERSION AFTER")
	if err = os.WriteFile(filepath.Join(p.WorkspacePath, "only-new.ts"), []byte("export const added = true;"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(p.WorkspacePath, ".env.local"), []byte("RESTORE_SENTINEL=current-private-config\n"), 0600); err != nil {
		t.Fatal(err)
	}
	request := func(method, suffix string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://localhost/api/project/"+p.ID+suffix, bytes.NewReader(raw))
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.signSession(owner.ID)})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	restore := func(id, expectedHash, marker string) {
		t.Helper()
		var revision int64
		if err := db.QueryRow(ctx, `SELECT source_revision FROM projects WHERE id=$1`, p.ID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		w := request("POST", "/restore", map[string]any{"version_id": id, "expected_revision": revision, "request_id": newID()})
		if w.Code != 202 {
			t.Fatalf("restore accepted: %d %s", w.Code, w.Body.String())
		}
		var result struct {
			ID string `json:"operation_id"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		for {
			op, e := scanRestore(db.QueryRow(ctx, `SELECT `+restoreColumns+` FROM project_restores WHERE id=$1`, result.ID))
			if e != nil {
				t.Fatal(e)
			}
			if op.Status == "FAILED" || op.Status == "BLOCKED" {
				t.Fatalf("real restore %s: %v (%s)", op.Status, op.ErrorMessage, op.Phase)
			}
			if op.Status == "COMPLETED" {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Second):
			}
		}
		s.restoreWorkers.Wait()
		manifest, e := sourceManifest(ctx, p.WorkspacePath)
		if e != nil || manifest.Hash != expectedHash {
			t.Fatalf("restored source mismatch: %v %s != %s", e, manifest.Hash, expectedHash)
		}
		w = request("GET", "/file?path=app/page.tsx", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), marker) {
			t.Fatalf("file panel source: %d %s", w.Code, w.Body.String())
		}
		out, e := s.docker.exec(ctx, runtimeName(p.ID), []string{"sh", "-lc", "curl --max-time 30 -fsS http://127.0.0.1:3000/"}, nil)
		if e != nil || !strings.Contains(string(out), marker) {
			t.Fatalf("actual Preview does not match %s: %v", marker, e)
		}
		var data string
		if e = db.QueryRow(ctx, fmt.Sprintf("SELECT value FROM %s.restore_sentinel", quoteIdent(p.DBSchema))).Scan(&data); e != nil || data != "business-data-kept" {
			t.Fatal("business data changed")
		}
		secret, e := os.ReadFile(filepath.Join(p.WorkspacePath, ".env.local"))
		if e != nil || !strings.Contains(string(secret), "current-private-config") {
			t.Fatal("private configuration changed")
		}
		t.Logf("restored %s: source hash, file API, actual homepage, private configuration and business data verified", marker)
	}
	restore(old, oldHash, "VERSION BEFORE")
	if _, err = os.Stat(filepath.Join(p.WorkspacePath, "only-new.ts")); !os.IsNotExist(err) {
		t.Fatal("new-only source remains")
	}
	restore(latest, latestHash, "VERSION AFTER")
	// Also exercise the unverified-but-canonical initial template snapshot.
	var initialID string
	if err = db.QueryRow(ctx, `SELECT id FROM project_versions WHERE project_id=$1 AND number=1`, p.ID).Scan(&initialID); err != nil {
		t.Fatal(err)
	}
	restore(initialID, initialHash, "ATOMS STARTER")
	restore(latest, latestHash, "VERSION AFTER")
	if os.Getenv("ATOMS_VERSIONS_BROWSER_FIXTURE") != "1" {
		return
	}

	// Optional real-browser phase shares only this disposable project's Runtime.
	stop := make(chan struct{})
	var once sync.Once
	var failHistory atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__fixture" || strings.HasPrefix(r.URL.Path, "/__fixture/") {
			auth.requireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/__fixture/stop":
					once.Do(func() { close(stop) })
				case "/__fixture/fail":
					failHistory.Store(true)
					http.Redirect(w, r, "/__fixture", 303)
				case "/__fixture/retry":
					failHistory.Store(false)
					http.Redirect(w, r, "/__fixture", 303)
				default:
					fmt.Fprintf(w, `<h1>Isolated Runtime browser fixture</h1><a href="/projects/%s">Project</a><form action="/__fixture/fail" method="post"><button>Fail history</button></form><form action="/__fixture/retry" method="post"><button>Recover history</button></form><form action="/__fixture/stop" method="post"><button>Stop fixture</button></form>`, p.ID)
				}
			})).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/project/"+p.ID+"/versions" && failHistory.Load() {
			auth.requireUser(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { apiError(w, 500, "VERSION_READ_FAILED") })).ServeHTTP(w, r)
			return
		}
		router.ServeHTTP(w, r)
	})
	listener, err := net.Listen("tcp", ":19091")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	defer server.Close()
	go server.Serve(listener)
	t.Logf("Browser fixture: http://versions-fixture.localhost:19091/projects/%s ; login %s / Versions-runtime-fixture-123!", p.ID, owner.Email)
	select {
	case <-stop:
	case <-time.After(15 * time.Minute):
	}
	// Keep the browser phase bounded separately from source verification timeout.
}
