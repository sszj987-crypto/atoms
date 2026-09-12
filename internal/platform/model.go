package platform

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type modelService struct {
	db     *pgxpool.Pool
	key    []byte
	client *http.Client
}

func newModelService(db *pgxpool.Pool, key []byte) *modelService {
	return &modelService{db: db, key: key, client: &http.Client{Timeout: 15 * time.Second}}
}

type modelInput struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}
type modelResponse struct {
	BaseURL   string `json:"base_url"`
	APIKeySet bool   `json:"api_key_set"`
	Model     string `json:"model"`
}

func validateModelInput(in modelInput) bool {
	u, err := url.ParseRequestURI(in.BaseURL)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && strings.TrimSpace(in.APIKey) != "" && strings.TrimSpace(in.Model) != ""
}

func (s *modelService) get(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(currentUserKey{}).(User)
	var base, model string
	var secret []byte
	err := s.db.QueryRow(r.Context(), `SELECT base_url,api_key_ciphertext,model FROM llm_configs WHERE user_id=$1`, u.ID).Scan(&base, &secret, &model)
	if err == pgx.ErrNoRows {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	if err != nil {
		apiError(w, 500, "CONFIG_READ_FAILED")
		return
	}
	writeJSON(w, 200, modelResponse{base, true, model})
}
func (s *modelService) put(w http.ResponseWriter, r *http.Request) {
	var in modelInput
	if decodeJSON(r, &in) != nil || !validateModelInput(in) {
		apiError(w, 400, "INVALID_MODEL_CONFIG")
		return
	}
	sealed, err := encrypt(s.key, strings.TrimSpace(in.APIKey))
	if err != nil {
		apiError(w, 500, "CONFIG_SAVE_FAILED")
		return
	}
	u := r.Context().Value(currentUserKey{}).(User)
	_, err = s.db.Exec(r.Context(), `INSERT INTO llm_configs(id,user_id,base_url,api_key_ciphertext,model) VALUES($1,$2,$3,$4,$5) ON CONFLICT(user_id) DO UPDATE SET base_url=EXCLUDED.base_url, api_key_ciphertext=EXCLUDED.api_key_ciphertext, model=EXCLUDED.model, updated_at=now()`, newID(), u.ID, strings.TrimRight(strings.TrimSpace(in.BaseURL), "/"), sealed, strings.TrimSpace(in.Model))
	if err != nil {
		apiError(w, 500, "CONFIG_SAVE_FAILED")
		return
	}
	writeJSON(w, 200, modelResponse{in.BaseURL, true, in.Model})
}
func (s *modelService) test(w http.ResponseWriter, r *http.Request) {
	var in modelInput
	if decodeJSON(r, &in) != nil {
		apiError(w, 400, "INVALID_MODEL_CONFIG")
		return
	}
	if in.APIKey == "" {
		u := r.Context().Value(currentUserKey{}).(User)
		var secret []byte
		err := s.db.QueryRow(r.Context(), `SELECT base_url,api_key_ciphertext,model FROM llm_configs WHERE user_id=$1`, u.ID).Scan(&in.BaseURL, &secret, &in.Model)
		if err != nil {
			apiError(w, 400, "MODEL_CONFIG_REQUIRED")
			return
		}
		in.APIKey, err = decrypt(s.key, secret)
		if err != nil {
			apiError(w, 500, "CONFIG_READ_FAILED")
			return
		}
	}
	if !validateModelInput(in) || !s.testResponses(r, in) {
		apiError(w, 422, "MODEL_SERVICE_UNAVAILABLE_OR_INCOMPATIBLE")
		return
	}
	writeJSON(w, 200, map[string]string{"message": "Connection successful"})
}
func (s *modelService) testResponses(r *http.Request, in modelInput) bool {
	body, _ := json.Marshal(map[string]any{"model": in.Model, "input": "Reply with OK.", "max_output_tokens": 1})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(in.BaseURL, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+in.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode >= 200 && res.StatusCode < 300
}
