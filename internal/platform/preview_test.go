package platform

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPreviewProxyRequiresEmbeddedHTMLAndScopesCookies(t *testing.T) {
	prefix, origin := "/__atoms_preview/project/cap", "http://example.test:8080"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "null" {
			t.Error("opaque Origin reached Next")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.SetCookie(w, &http.Cookie{Name: "app_session", Value: "business", Path: "/"})
		w.Write([]byte(`<!doctype html><html><head></head><body><img src="/photo.png"><a href="/about">About</a><script src="/__atoms_preview/project/cap/_next/static/main.js"></script><img src="//cdn.example.test/image.png"></body></html>`))
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	proxy := newPreviewProxy(target)
	restrictPreviewProxy(proxy, prefix, origin, "/projects/project")
	for _, tc := range []struct {
		referer string
		want    int
	}{{"", 403}, {"http://evil.test/", 403}, {origin + "/projects/other", 403}, {origin + "/projects/project", 200}, {origin + prefix + "/", 200}} {
		r := httptest.NewRequest("GET", origin+prefix+"/", nil)
		r.Header.Set("Referer", tc.referer)
		r.Header.Set("Origin", "null")
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("referer %q: %d %s", tc.referer, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") || strings.Contains(w.Header().Get("Content-Security-Policy"), "allow-same-origin") {
			t.Fatal("preview shares platform privileges")
		}
		if cookies := w.Result().Cookies(); len(cookies) != 1 || cookies[0].Path != prefix+"/" {
			t.Fatalf("business cookie escaped project path: %v", cookies)
		}
		if tc.want == 200 && (!strings.Contains(w.Body.String(), `src="`+prefix+`/photo.png"`) || !strings.Contains(w.Body.String(), "atoms-preview-request")) {
			t.Fatal("viewer lost paths or request bridge")
		}
		if tc.want == 200 && (strings.Contains(w.Body.String(), prefix+prefix) || !strings.Contains(w.Body.String(), `src="//cdn.example.test/image.png"`)) {
			t.Fatal("existing or external resource URL was corrupted")
		}
	}
}

func TestPreviewProxyPreservesRoutesAndIsolatesCookies(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/api/items?q=a%2Fb" || r.Method != "POST" {
			t.Errorf("route changed: %s %s", r.Method, r.URL.RequestURI())
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "business request" {
			t.Errorf("request body = %q", body)
		}
		if r.Header.Get("Cookie") != "app_session=business" {
			t.Errorf("credentials leaked: %q", r.Header.Get("Cookie"))
		}
		if r.Host != "p-project.example.com" || r.Header.Get("Origin") != "http://"+r.Host || r.Header.Get("X-Forwarded-Host") != r.Host {
			t.Errorf("Next host/origin mismatch")
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "overwrite", Path: "/", Domain: ".example.com"})
		http.SetCookie(w, &http.Cookie{Name: previewCookie, Value: "overwrite", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "app_session", Value: "updated", Domain: ".example.com", Path: "/"})
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("business response"))
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	r := httptest.NewRequest("POST", "http://p-project.example.com/api/items?q=a%2Fb", strings.NewReader("business request"))
	r.Header.Set("Origin", "http://p-project.example.com")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "platform secret"})
	r.AddCookie(&http.Cookie{Name: previewCookie, Value: "preview secret"})
	r.AddCookie(&http.Cookie{Name: "app_session", Value: "business"})
	w := httptest.NewRecorder()
	newPreviewProxy(target).ServeHTTP(w, r)
	if w.Code != http.StatusCreated || w.Body.String() != "business response" {
		t.Fatalf("response = %d %q", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "app_session" || cookies[0].Domain != "" {
		t.Fatalf("unsafe response cookies: %#v", cookies)
	}
}

func TestPreviewAuthenticationIntegration(t *testing.T) {
	dsn := os.Getenv("ATOMS_FILES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires a disposable ATOMS_FILES_TEST_DATABASE_URL")
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
	owner, other := User{ID: newID(), Email: newID() + "@example.test"}, User{ID: newID(), Email: newID() + "@example.test"}
	for _, u := range []User{owner, other} {
		if _, err = db.Exec(ctx, `INSERT INTO users(id,email,password_hash) VALUES($1,$2,'fixture')`, u.ID, u.Email); err != nil {
			t.Fatal(err)
		}
	}
	defer db.Exec(ctx, `DELETE FROM users WHERE id=$1 OR id=$2`, owner.ID, other.ID)
	p := Project{ID: newID(), WorkspacePath: t.TempDir()}
	if _, err = db.Exec(ctx, `INSERT INTO projects(id,user_id,name,workspace_path,codex_state_path,last_accessed_at,db_schema,db_username,db_password_ciphertext) VALUES($1,$2,'preview fixture',$3,$3,now(),'fixture','fixture',$4)`, p.ID, owner.ID, p.WorkspacePath, []byte{}); err != nil {
		t.Fatal(err)
	}
	auth := newAuthService(db, bytes.Repeat([]byte{1}, 32), 18000, 5)
	s := newProjectService(db, Config{}, nil)
	defer s.shutdownRestores(ctx)
	app := &App{db: db, auth: auth, projects: s, cfg: Config{WebDir: t.TempDir()}}
	router := app.Router()
	prefix := previewPath(app.cfg.MasterKey, p.ID)
	request := func(path, origin, dest string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8080"+path, nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Sec-Fetch-Dest", dest)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.signSession(owner.ID)})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := request(prefix+"/", "", "iframe"); w.Code != 401 {
		t.Fatalf("no active lease: %d", w.Code)
	}
	app.previewGateway().lease(p.ID, owner.ID)
	for _, tc := range []struct {
		path, origin, dest string
		code               int
	}{
		{prefix + "/", "", "document", 403},
		{previewNamespace + p.ID + "/wrong/", "", "iframe", 401},
		{prefix + "/", "http://evil.example", "iframe", 403},
	} {
		if w := request(tc.path, tc.origin, tc.dest); w.Code != tc.code {
			t.Fatalf("deny: %d want %d", w.Code, tc.code)
		}
	}
	app.previewGateway().lease(p.ID, other.ID)
	if w := request(prefix+"/", "", "iframe"); w.Code != 404 {
		t.Fatalf("wrong owner: %d", w.Code)
	}
	app.previewGateway().lease(p.ID, owner.ID)
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/auth/logout", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: auth.signSession(owner.ID)})
	router.ServeHTTP(httptest.NewRecorder(), r)
	if app.previewGateway().owner(p.ID) != "" {
		t.Fatal("logout retained preview access")
	}
	loaded, err := s.byID(ctx, owner.ID, p.ID)
	if err != nil || loaded.Deployed {
		t.Fatalf("new project publication: %+v %v", loaded, err)
	}
}
