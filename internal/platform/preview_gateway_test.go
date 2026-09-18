package platform

import (
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestPreviewGatewayAllocationAndIsolation(t *testing.T) {
	app := &App{cfg: Config{PreviewPortRange: "19001-19002"}, auth: &authService{sessionKey: []byte("fixture")}}
	g := app.previewGateway()
	r := httptest.NewRequest("GET", "http://203.0.113.10:8080/api", nil)
	first, err := g.address(r, "first", "ticket-a")
	if err != nil {
		t.Fatal(err)
	}
	if first != "http://203.0.113.10:19001/?preview_token=ticket-a" {
		t.Fatal(first)
	}
	again, _ := g.address(r, "first", "ticket-b")
	u, _ := url.Parse(again)
	if u.Port() != "19001" {
		t.Fatal("project lost its active preview slot")
	}
	second, _ := g.address(r, "second", "ticket")
	u, _ = url.Parse(second)
	if u.Port() != "19002" {
		t.Fatal("concurrent projects share an origin")
	}
	if _, err = g.address(r, "third", "ticket"); err == nil {
		t.Fatal("allocated beyond preview capacity")
	}
	g.slots["first"] = previewSlot{port: "19001", accessed: time.Now().Add(-31 * time.Minute)}
	third, err := g.address(r, "third", "ticket")
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(third)
	if u.Port() != "19001" || g.touch("first", "19001") {
		t.Fatal("idle slot was not safely reassigned")
	}
	// A stale project's capability cannot open the new occupant's app.
	request := httptest.NewRequest("GET", third, nil)
	query := request.URL.Query()
	query.Set("preview_token", app.auth.signPreview("owner", "first"))
	request.URL.RawQuery = query.Encode()
	w := httptest.NewRecorder()
	app.PreviewHandler("19001").ServeHTTP(w, request)
	if w.Code != 401 {
		t.Fatalf("stale preview reached a different project: %d", w.Code)
	}
	for _, port := range []string{"19001", "19002", "19003"} {
		w = httptest.NewRecorder()
		app.PreviewHandler(port).ServeHTTP(w, httptest.NewRequest("GET", "http://203.0.113.10:"+port+"/", nil))
		if w.Code != 401 {
			t.Fatalf("anonymous gateway %s: %d", port, w.Code)
		}
	}
}

func TestPreviewPortRange(t *testing.T) {
	for _, raw := range []string{"", "8081-8100", "19000", "19000-19099"} {
		if _, err := parsePreviewPorts(raw); err != nil {
			t.Fatalf("valid range %q: %v", raw, err)
		}
	}
	for _, raw := range []string{"0-10", "8100-8081", "65535-65536", "8081-8181", "foo"} {
		if _, err := parsePreviewPorts(raw); err == nil {
			t.Fatalf("accepted invalid range %q", raw)
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
