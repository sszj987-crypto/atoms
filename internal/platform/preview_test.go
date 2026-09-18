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
	request := httptest.NewRequest("GET", "http://127.0.0.1:8080/api", nil)
	_, err = app.previewGateway().address(request, p.ID, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	router := app.PreviewHandler("8081")
	entry := "http://127.0.0.1:8081/"
	for _, tc := range []struct {
		name, token, origin string
		status              int
	}{
		{"anonymous", "", "", 401},
		{"tampered", auth.signPreview(owner.ID, p.ID) + "broken", "", 401},
		{"another project", auth.signPreview(owner.ID, newID()), "", 401},
		{"another owner", auth.signPreview(other.ID, p.ID), "", 404},
		{"cross origin", auth.signPreview(owner.ID, p.ID), "http://evil.example", 403},
		{"owner", auth.signPreview(owner.ID, p.ID), "", 303},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", entry+"?business=kept&preview_token="+url.QueryEscape(tc.token), nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}
			if tc.status == 303 {
				if w.Header().Get("Location") != "/?business=kept" {
					t.Fatalf("ticket not stripped: %s", w.Header().Get("Location"))
				}
				cookies := w.Result().Cookies()
				if len(cookies) != 1 || cookies[0].Name != previewCookie+"_8081" || !cookies[0].HttpOnly || cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
					t.Fatalf("invalid preview cookie: %#v", cookies)
				}
				uid, pid, ok := auth.readPreview(cookies[0].Value)
				if !ok || uid != owner.ID || pid != p.ID {
					t.Fatal("invalid preview session")
				}
			}
		})
	}
	loaded, err := s.byID(ctx, owner.ID, p.ID)
	if err != nil || loaded.Deployed {
		t.Fatalf("new projects must be private: %+v %v", loaded, err)
	}
}
