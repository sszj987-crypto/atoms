package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Run in an isolated socket-free container alongside a manually started Next
// fixture. Runtime lifecycle responses are simulated; proxy traffic goes to
// the real app. The test program has no Docker authority.
func TestPreviewGatewayBrowserFixture(t *testing.T) {
	if os.Getenv("ATOMS_PREVIEW_BROWSER_FIXTURE") != "1" {
		t.Skip("requires an isolated socket-free fixture")
	}
	dsn, id := os.Getenv("ATOMS_PREVIEW_FIXTURE_DATABASE_URL"), os.Getenv("ATOMS_PREVIEW_FIXTURE_PROJECT_ID")
	if dsn == "" || !validID(id) {
		t.Fatal("disposable fixture database and project ID are required")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{4}, 32)
	owner := User{ID: newID(), Email: "preview-gateway@example.test"}
	password, err := hashPassword("Preview-fixture-123!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO users(id,email,password_hash,deploy_port) VALUES($1,$2,$3,19410)`, owner.ID, owner.Email, password); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, `DELETE FROM users WHERE id=$1`, owner.ID)
	cipher, _ := encrypt(key, "fixture")
	if _, err = db.Exec(ctx, `INSERT INTO projects(id,user_id,name,workspace_path,codex_state_path,last_accessed_at,db_schema,db_username,db_password_ciphertext,deploy_port) VALUES($1,$2,'鉴权预览测试','/workspace','/tmp/fixture-codex',now(),'fixture','fixture',$3,19410)`, id, owner.ID, cipher); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	running, published, failCreate := true, false, false
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/json"):
			bindings := map[string]any{}
			if published {
				bindings["3000/tcp"] = []map[string]string{{"HostPort": "19410"}}
			}
			writeJSON(w, 200, map[string]any{"State": map[string]bool{"Running": running}, "HostConfig": map[string]any{"PortBindings": bindings}})
		case r.Method == "DELETE":
			running = false
			published = false
			w.WriteHeader(204)
		case r.URL.Path == "/containers/create":
			if failCreate {
				failCreate = false
				writeJSON(w, 500, map[string]string{"message": "fixture create failed"})
				return
			}
			var body struct {
				HostConfig struct{ PortBindings map[string]any }
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			published = len(body.HostConfig.PortBindings) > 0
			w.WriteHeader(201)
		case strings.HasSuffix(r.URL.Path, "/start"):
			running = true
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer mock.Close()
	cfg := Config{MasterKey: key, PreviewPortRange: "19510-19511", WebDir: "/test-web", RuntimeImage: "fixture", RuntimeNetwork: "fixture", DataVolumeName: "fixture"}
	s := newProjectService(db, cfg, nil)
	defer s.shutdownRestores(ctx)
	s.docker = &dockerClient{client: &http.Client{Transport: rewriteDockerTransport{base: mock.URL}}}
	auth := newAuthService(db, key, 19410, 2)
	app := &App{db: db, auth: auth, projects: s, chat: newChatService(s), cfg: cfg}
	router := app.Router()
	request := func(method, suffix string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:19091/api/project/"+id+suffix, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.signSession(owner.ID)})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	stop := make(chan struct{})
	var once sync.Once
	platformHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__fixture/stop" {
			auth.requireUser(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { once.Do(func() { close(stop) }); w.WriteHeader(204) })).ServeHTTP(w, r)
			return
		}
		router.ServeHTTP(w, r)
	})
	for _, port := range append([]string{"19091"}, app.PreviewPorts()...) {
		var handler http.Handler = app.PreviewHandler(port)
		if port == "19091" {
			handler = platformHandler
		}
		listener, e := net.Listen("tcp", ":"+port)
		if e != nil {
			t.Fatal(e)
		}
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
		defer server.Close()
		go server.Serve(listener)
	}
	if err = waitRuntimeReady(ctx, id); err != nil {
		t.Fatal(err)
	}
	access := request("GET", "/preview-access")
	if access.Code != 200 {
		t.Fatalf("access: %d %s", access.Code, access.Body.String())
	}
	var entry struct{ URL string }
	if err = json.Unmarshal(access.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	response, err := client.Get(entry.URL)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !bytes.Contains(page, []byte("Your app is ready")) {
		t.Fatalf("real homepage: %d", response.StatusCode)
	}
	clean := *response.Request.URL
	if strings.Contains(clean.RawQuery, "preview_token") {
		t.Fatal("bootstrap token reached application URL")
	}
	response, err = http.Get(clean.String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("anonymous preview: %d", response.StatusCode)
	}
	clean.Path = "/api/health"
	response, err = client.Get(clean.String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("real API route: %d", response.StatusCode)
	}
	mu.Lock()
	failCreate = true
	mu.Unlock()
	if w := request("POST", "/deploy"); w.Code != 503 {
		t.Fatalf("failure: %d %s", w.Code, w.Body.String())
	}
	loaded, err := s.byID(ctx, owner.ID, id)
	if err != nil || loaded.Deployed {
		t.Fatal("failed deployment persisted publication")
	}
	mu.Lock()
	exposed := published
	mu.Unlock()
	if exposed {
		t.Fatal("failed deployment left published port bindings")
	}
	if w := request("POST", "/deploy"); w.Code != 200 {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	loaded, err = s.byID(ctx, owner.ID, id)
	if err != nil || !loaded.Deployed {
		t.Fatal("deployment was not persisted")
	}
	if w := request("POST", "/runtime/restart"); w.Code != 200 {
		t.Fatalf("restart: %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	exposed = published
	mu.Unlock()
	if !exposed {
		t.Fatal("restart lost deployment bindings")
	}
	t.Log("real Next homepage/API and anonymous denial passed; simulated Runtime deployment failure, publication and restart passed")
	t.Logf("Browser fixture: http://preview-fixture.localhost:19091/projects/%s ; login preview-gateway@example.test / Preview-fixture-123!", id)
	select {
	case <-stop:
	case <-time.After(15 * time.Minute):
	}
}
