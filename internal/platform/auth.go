package platform

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	sessionCookie = "atoms_session"
	previewCookie = "atoms_preview"
)

type currentUserKey struct{}
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}
type authService struct {
	db             *pgxpool.Pool
	sessionKey     []byte
	deployPortBase int
	deployPortSpan int
}

func newAuthService(db *pgxpool.Pool, key []byte, portBase, portSpan int) *authService {
	return &authService{db: db, sessionKey: key, deployPortBase: portBase, deployPortSpan: portSpan}
}

func (s *authService) register(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if decodeJSON(r, &input) != nil || !validEmail(input.Email) {
		apiError(w, http.StatusBadRequest, "INVALID_REGISTRATION")
		return
	}
	hash, err := hashPassword(input.Password)
	if err != nil {
		apiError(w, http.StatusBadRequest, "INVALID_PASSWORD")
		return
	}
	u := User{ID: newID(), Email: strings.ToLower(strings.TrimSpace(input.Email))}
	_, err = s.db.Exec(r.Context(), `INSERT INTO users (id,email,password_hash,deploy_port) SELECT $1,$2,$3,$4 + (nextval('user_port_seq') - 1) * $5`, u.ID, u.Email, hash, s.deployPortBase, s.deployPortSpan)
	if err != nil {
		apiError(w, http.StatusConflict, "EMAIL_ALREADY_REGISTERED")
		return
	}
	s.setSession(w, u.ID)
	writeJSON(w, http.StatusCreated, u)
}

func (s *authService) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if decodeJSON(r, &input) != nil {
		apiError(w, http.StatusBadRequest, "INVALID_LOGIN")
		return
	}
	var u User
	var passwordHash string
	err := s.db.QueryRow(r.Context(), `SELECT id,email,password_hash FROM users WHERE email=$1`, strings.ToLower(strings.TrimSpace(input.Email))).Scan(&u.ID, &u.Email, &passwordHash)
	if err != nil || !verifyPassword(passwordHash, input.Password) {
		apiError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS")
		return
	}
	s.setSession(w, u.ID)
	writeJSON(w, http.StatusOK, u)
}
func (s *authService) logout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Domain: "localhost", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}
func (s *authService) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, r.Context().Value(currentUserKey{}).(User))
}

func (s *authService) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			apiError(w, http.StatusUnauthorized, "AUTH_REQUIRED")
			return
		}
		userID, ok := s.readSession(cookie.Value)
		if !ok {
			apiError(w, http.StatusUnauthorized, "AUTH_REQUIRED")
			return
		}
		var u User
		if err := s.db.QueryRow(r.Context(), `SELECT id,email FROM users WHERE id=$1`, userID).Scan(&u.ID, &u.Email); err != nil {
			apiError(w, http.StatusUnauthorized, "AUTH_REQUIRED")
			return
		}
		next.ServeHTTP(w, contextWithUser(r, u))
	})
}
func contextWithUser(r *http.Request, u User) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), currentUserKey{}, u))
}
func (s *authService) setSession(w http.ResponseWriter, userID string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: s.signSession(userID), Domain: "localhost", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: false, MaxAge: int((7 * 24 * time.Hour).Seconds())})
}
func (s *authService) signSession(id string) string {
	payload, _ := json.Marshal(struct {
		ID  string `json:"id"`
		Exp int64  `json:"exp"`
	}{id, time.Now().Add(7 * 24 * time.Hour).Unix()})
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.sessionKey)
	mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *authService) readSession(v string) (string, bool) {
	parts := strings.Split(v, ".")
	if len(parts) != 2 {
		return "", false
	}
	mac := hmac.New(sha256.New, s.sessionKey)
	mac.Write([]byte(parts[0]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return "", false
	}
	var p struct {
		ID  string `json:"id"`
		Exp int64  `json:"exp"`
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &p) != nil || p.Exp < time.Now().Unix() {
		return "", false
	}
	return p.ID, true
}
func (s *authService) signPreview(userID, projectID string) string {
	payload, _ := json.Marshal(struct {
		UserID    string `json:"uid"`
		ProjectID string `json:"pid"`
		Exp       int64  `json:"exp"`
	}{userID, projectID, time.Now().Add(10 * time.Minute).Unix()})
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.sessionKey)
	mac.Write([]byte("preview." + encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *authService) readPreview(v string) (string, string, bool) {
	parts := strings.Split(v, ".")
	if len(parts) != 2 {
		return "", "", false
	}
	mac := hmac.New(sha256.New, s.sessionKey)
	mac.Write([]byte("preview." + parts[0]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return "", "", false
	}
	var payload struct {
		UserID    string `json:"uid"`
		ProjectID string `json:"pid"`
		Exp       int64  `json:"exp"`
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &payload) != nil || payload.UserID == "" || payload.ProjectID == "" || payload.Exp < time.Now().Unix() {
		return "", "", false
	}
	return payload.UserID, payload.ProjectID, true
}
func (s *authService) setPreviewCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     previewCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   false,
		MaxAge:   int((10 * time.Minute).Seconds()),
	})
}
func validEmail(v string) bool {
	v = strings.TrimSpace(v)
	return len(v) <= 254 && strings.Count(v, "@") == 1 && !strings.HasPrefix(v, "@") && !strings.HasSuffix(v, "@")
}
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
