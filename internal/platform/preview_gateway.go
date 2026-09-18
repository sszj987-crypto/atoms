package platform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const previewNamespace = "/__atoms_preview/"

type previewGateway struct {
	mu    sync.Mutex
	slots map[string]previewSlot
}
type previewSlot struct {
	userID  string
	expires time.Time
}

func previewPath(key []byte, projectID string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("atoms-private-preview-v2:" + projectID))
	return previewNamespace + projectID + "/" + hex.EncodeToString(mac.Sum(nil)[:16])
}

func (a *App) previewGateway() *previewGateway {
	a.gatewayOnce.Do(func() { a.gateway = &previewGateway{slots: make(map[string]previewSlot)} })
	return a.gateway
}
func (g *previewGateway) lease(projectID, userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for id, slot := range g.slots {
		if time.Now().After(slot.expires) {
			delete(g.slots, id)
		}
	}
	g.slots[projectID] = previewSlot{userID: userID, expires: time.Now().Add(30 * time.Minute)}
}
func (g *previewGateway) owner(projectID string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	slot := g.slots[projectID]
	if time.Now().After(slot.expires) {
		delete(g.slots, projectID)
		return ""
	}
	return slot.userID
}
func (g *previewGateway) revoke(userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for id, slot := range g.slots {
		if slot.userID == userID {
			delete(g.slots, id)
		}
	}
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if secureRequest(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
