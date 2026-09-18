package platform

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type previewFixtureTransport struct{ projectID string }

func (t previewFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Sec-Fetch-Dest", "iframe")
	r.Header.Set("Referer", r.URL.Scheme+"://"+r.URL.Host+"/projects/"+t.projectID)
	return http.DefaultTransport.RoundTrip(r)
}
func TestPreviewLeaseIsolation(t *testing.T) {
	app := &App{cfg: Config{MasterKey: []byte("fixture")}}
	g := app.previewGateway()
	g.lease("first", "owner-a")
	g.lease("second", "owner-b")
	if g.owner("first") != "owner-a" || g.owner("second") != "owner-b" {
		t.Fatal("project leases overlap")
	}
	if previewPath(app.cfg.MasterKey, "first") == previewPath(app.cfg.MasterKey, "second") {
		t.Fatal("projects share a route")
	}
	g.slots["first"] = previewSlot{userID: "owner-a", expires: time.Now().Add(-time.Second)}
	if g.owner("first") != "" {
		t.Fatal("expired lease is still usable")
	}
	g.revoke("owner-b")
	if g.owner("second") != "" {
		t.Fatal("logout left a preview lease")
	}
}
func TestPreviewCannotOpenStandalone(t *testing.T) {
	app := &App{cfg: Config{MasterKey: []byte("fixture")}}
	id := "11111111-2222-4333-8444-555555555555"
	path := previewPath(app.cfg.MasterKey, id) + "/"
	for _, tc := range []struct {
		dest string
		want int
	}{{"document", 403}, {"iframe", 401}, {"", 401}} {
		r := httptest.NewRequest("GET", "http://example.test:8080"+path, nil)
		r.Header.Set("Sec-Fetch-Dest", tc.dest)

		w := httptest.NewRecorder()
		app.Router().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("direct preview status %d want %d", w.Code, tc.want)
		}
	}
}
func TestPlatformRejectsPreviewOrigin(t *testing.T) {
	app := &App{auth: &authService{}, cfg: Config{WebDir: t.TempDir()}}
	request := httptest.NewRequest("GET", "http://localhost:8080/api/me", nil)
	request.Header.Set("Origin", "http://localhost:8081")
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatalf("preview can send authenticated API requests: %d", w.Code)
	}
}
