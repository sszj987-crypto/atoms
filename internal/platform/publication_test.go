package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Run without a Docker socket in a disposable container whose network alias
// is runtimeName(ATOMS_PUBLICATION_TEST_PROJECT_ID). A TCP fixture satisfies
// readiness; the Docker API is simulated and publication state uses real SQL.
func TestPublicationLifecycleIntegration(t *testing.T) {
	id, dsn := os.Getenv("ATOMS_PUBLICATION_TEST_PROJECT_ID"), os.Getenv("ATOMS_FILES_TEST_DATABASE_URL")
	if !validID(id) || dsn == "" {
		t.Skip("requires a disposable database and a container runtime alias")
	}
	listener, err := net.Listen("tcp", ":3000")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	previewServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("private preview")) })}
	defer previewServer.Close()
	go previewServer.Serve(listener)
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	owner, other := User{ID: newID()}, User{ID: newID()}
	for _, u := range []User{owner, other} {
		if _, err = db.Exec(ctx, `INSERT INTO users(id,email,password_hash) VALUES($1,$2,'fixture')`, u.ID, u.ID+"@example.test"); err != nil {
			t.Fatal(err)
		}
	}
	defer db.Exec(ctx, `DELETE FROM users WHERE id=$1 OR id=$2`, owner.ID, other.ID)
	key := bytes.Repeat([]byte{5}, 32)
	cipher, _ := encrypt(key, "fixture")
	if _, err = db.Exec(ctx, `INSERT INTO projects(id,user_id,name,workspace_path,codex_state_path,last_accessed_at,db_schema,db_username,db_password_ciphertext,deploy_port,deployed) VALUES($1,$2,'publication fixture','/fixture/workspace','/fixture/codex',now(),'fixture','fixture',$3,18450,true)`, id, owner.ID, cipher); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	published, running, failCreate := true, true, false
	mutations := 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/json"):
			bindings := map[string]any{}
			if published {
				bindings["3000/tcp"] = []map[string]string{{"HostPort": "18450"}}
			}
			writeJSON(w, 200, map[string]any{"State": map[string]bool{"Running": running}, "HostConfig": map[string]any{"PortBindings": bindings}})
		case r.Method == "DELETE":
			mutations++
			published = false
			running = false
			w.WriteHeader(204)
		case r.URL.Path == "/containers/create":
			mutations++
			if failCreate {
				failCreate = false
				w.WriteHeader(500)
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
	cfg := Config{MasterKey: key, RuntimeImage: "fixture", RuntimeNetwork: "fixture", DataVolumeName: "fixture", WebDir: t.TempDir()}
	s := newProjectService(db, cfg, nil)
	defer s.shutdownRestores(ctx)
	s.docker = &dockerClient{client: &http.Client{Transport: rewriteDockerTransport{base: mock.URL}}}
	auth := newAuthService(db, key, 18450, 5)
	app := &App{db: db, auth: auth, projects: s, chat: newChatService(s), cfg: cfg}
	router := app.Router()
	request := func(suffix string, u *User) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost/api/project/"+id+suffix, nil)
		if u != nil {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.signSession(u.ID)})
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	assertState := func(want bool) {
		t.Helper()
		loaded, err := s.byID(ctx, owner.ID, id)
		if err != nil || loaded.Deployed != want {
			t.Fatalf("persisted publication: %+v %v, want %v", loaded, err, want)
		}
		mu.Lock()
		actual := published
		mu.Unlock()
		if actual != want {
			t.Fatalf("port publication = %v, want %v", actual, want)
		}
	}
	for _, tc := range []struct {
		user *User
		code int
	}{{nil, 401}, {&other, 404}} {
		if w := request("/undeploy", tc.user); w.Code != tc.code {
			t.Fatalf("ownership: %d %s", w.Code, w.Body.String())
		}
	}
	runID := newID()
	if _, err = db.Exec(ctx, `INSERT INTO agent_runs(id,project_id,status) VALUES($1,$2,'RUNNING')`, runID, id); err != nil {
		t.Fatal(err)
	}
	if w := request("/undeploy", &owner); w.Code != 409 {
		t.Fatalf("busy project: %d %s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `UPDATE agent_runs SET status='COMPLETED' WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	count := mutations
	failCreate = true
	mu.Unlock()
	if count != 0 {
		t.Fatal("denied requests mutated Runtime")
	}
	if w := request("/undeploy", &owner); w.Code != 503 {
		t.Fatalf("unpublish failure: %d %s", w.Code, w.Body.String())
	}
	assertState(true)
	for _, endpoint := range []string{"/undeploy", "/undeploy", "/runtime/restart"} {
		if w := request(endpoint, &owner); w.Code != 200 {
			t.Fatalf("%s: %d %s", endpoint, w.Code, w.Body.String())
		}
		assertState(false)
	}
	// Unpublishing keeps authenticated proxy traffic working without allowing
	// anonymous visitors to use the private application's preview gateway.
	access := httptest.NewRequest("GET", "http://localhost/api/project/"+id+"/preview-access", nil)
	access.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.signSession(owner.ID)})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, access)
	var entry struct{ URL string }
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &entry) != nil {
		t.Fatalf("private preview access: %d %s", response.Code, response.Body.String())
	}
	gateway := app.PreviewHandler("8081")
	bootstrap := httptest.NewRecorder()
	gateway.ServeHTTP(bootstrap, httptest.NewRequest("GET", entry.URL, nil))
	if bootstrap.Code != http.StatusSeeOther || len(bootstrap.Result().Cookies()) != 1 {
		t.Fatalf("preview bootstrap: %d %s", bootstrap.Code, bootstrap.Body.String())
	}
	for _, authenticated := range []bool{false, true} {
		r := httptest.NewRequest("GET", "http://localhost:8081/", nil)
		want := 401
		if authenticated {
			r.AddCookie(bootstrap.Result().Cookies()[0])
			want = 200
		}
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, r)
		if w.Code != want || authenticated && w.Body.String() != "private preview" {
			t.Fatalf("preview after unpublish: %d %s", w.Code, w.Body.String())
		}
	}
	mu.Lock()
	failCreate = true
	mu.Unlock()
	if w := request("/deploy", &owner); w.Code != 503 {
		t.Fatalf("republish failure: %d %s", w.Code, w.Body.String())
	}
	assertState(false)
	if w := request("/deploy", &owner); w.Code != 200 {
		t.Fatalf("republish: %d %s", w.Code, w.Body.String())
	}
	assertState(true)
}
