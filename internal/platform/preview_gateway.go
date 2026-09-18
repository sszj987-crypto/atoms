package platform

import (
	"context"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Each active preview receives its own origin (port). This preserves root
// routes, works with server IPs, and prevents two projects in separate tabs
// from sharing a routing cookie. These ports expose only the authenticated
// control-plane proxy, never the application container.
type previewGateway struct {
	mu    sync.Mutex
	slots map[string]previewSlot
	ports []string
}
type previewSlot struct {
	port     string
	accessed time.Time
}

func (a *App) previewGateway() *previewGateway {
	a.gatewayOnce.Do(func() {
		ports, _ := parsePreviewPorts(a.cfg.PreviewPortRange)
		a.gateway = &previewGateway{slots: make(map[string]previewSlot), ports: ports}
	})
	return a.gateway
}

func parsePreviewPorts(raw string) ([]string, error) {
	if raw == "" {
		raw = "8081-8100"
	}
	first, last, ok := strings.Cut(raw, "-")
	if !ok {
		last = first
	}
	start, e1 := strconv.Atoi(first)
	end, e2 := strconv.Atoi(last)
	if e1 != nil || e2 != nil || start < 1 || end > 65535 || end < start || end-start >= 100 {
		return nil, fmt.Errorf("PREVIEW_PORT_RANGE must contain 1-100 ports within 1-65535")
	}
	var ports []string
	for port := start; port <= end; port++ {
		ports = append(ports, strconv.Itoa(port))
	}
	return ports, nil
}

func (g *previewGateway) address(r *http.Request, projectID, token string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	slot, exists := g.slots[projectID]
	if !exists {
		used := make(map[string]bool)
		for id, s := range g.slots {
			if now.Sub(s.accessed) > 30*time.Minute {
				delete(g.slots, id)
			} else {
				used[s.port] = true
			}
		}
		for _, port := range g.ports {
			if !used[port] {
				slot.port = port
				break
			}
		}
		if slot.port == "" {
			return "", fmt.Errorf("preview capacity reached")
		}
	}
	slot.accessed = now
	g.slots[projectID] = slot
	scheme := "http"
	if secureRequest(r) {
		scheme = "https"
	}
	base, err := url.Parse(scheme + "://" + r.Host)
	if err != nil || base.Hostname() == "" {
		return "", fmt.Errorf("invalid preview request host")
	}
	base.Host = net.JoinHostPort(base.Hostname(), slot.port)
	base.Path = "/"
	base.RawQuery = url.Values{"preview_token": {token}}.Encode()
	return base.String(), nil
}

func (g *previewGateway) project(port string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	for id, s := range g.slots {
		if s.port == port {
			return id
		}
	}
	return ""
}
func (g *previewGateway) touch(projectID, port string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	s, ok := g.slots[projectID]
	if !ok || s.port != port {
		return false
	}
	s.accessed = time.Now()
	g.slots[projectID] = s
	return true
}

func (a *App) PreviewPorts() []string { return append([]string(nil), a.previewGateway().ports...) }

func (a *App) PreviewHandler(port string) http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.RealIP, hsts, middleware.Recoverer)
	router.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		projectID := a.previewGateway().project(port)
		if projectID == "" {
			apiError(w, http.StatusUnauthorized, "AUTH_REQUIRED")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), previewGatewayPortKey{}, port))
		a.servePreview(w, r, projectID, previewCookie+"_"+port)
	}))
	return router
}

type previewProjectKey struct{}
type previewGatewayPortKey struct{}
