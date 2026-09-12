package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	errModelConfigRequired   = errors.New("model configuration is required")
	errModelTitleUnavailable = errors.New("model title is unavailable")
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

func (s *modelService) projectTitle(ctx context.Context, userID, description string) (string, error) {
	var in modelInput
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT base_url,api_key_ciphertext,model FROM llm_configs WHERE user_id=$1`, userID).Scan(&in.BaseURL, &sealed, &in.Model)
	if err != nil {
		return "", errModelConfigRequired
	}
	in.APIKey, err = decrypt(s.key, sealed)
	if err != nil || !validateModelInput(in) {
		return "", errModelConfigRequired
	}
	body, _ := json.Marshal(map[string]any{
		"model":             in.Model,
		"instructions":      "只返回项目名称本身。不要复述用户要求、需求内容、提示词或任何说明。",
		"input":             "根据下面的 Web 应用需求，生成一个简洁、具体的中文项目名称。名称不超过 16 个汉字或 32 个字符。只输出名称本身，不要解释、引号、序号或标点。\n\n需求：\n" + strings.TrimSpace(description),
		"max_output_tokens": 48,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(in.BaseURL, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return "", errModelTitleUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+in.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return "", errModelTitleUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", errModelTitleUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return "", errModelTitleUnavailable
	}
	var response struct {
		OutputText json.RawMessage `json:"output_text"`
		Output     []struct {
			Content []struct {
				Text json.RawMessage `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return "", errModelTitleUnavailable
	}
	title := responseText(response.OutputText)
	if title == "" {
		for _, output := range response.Output {
			for _, content := range output.Content {
				if text := responseText(content.Text); text != "" {
					title = text
					break
				}
			}
			if title != "" {
				break
			}
		}
	}
	if title == "" && len(response.Choices) > 0 {
		title = responseText(response.Choices[0].Message.Content)
	}
	title = strings.Trim(strings.TrimSpace(strings.Split(title, "\n")[0]), " \t\"'“”‘’「」")
	if title == "" {
		return "", errModelTitleUnavailable
	}
	runes := []rune(title)
	if len(runes) > 32 {
		title = string(runes[:32])
	}
	if !usableProjectTitle(title) {
		return "", errModelTitleUnavailable
	}
	return title, nil
}

func usableProjectTitle(title string) bool {
	title = strings.TrimSpace(title)
	if title == "" || strings.ContainsAny(title, ":：\n\r") {
		return false
	}
	for _, prefix := range []string{"用户要求", "项目名称", "为 Web 应用", "为web应用"} {
		if strings.HasPrefix(strings.ToLower(title), strings.ToLower(prefix)) {
			return false
		}
	}
	return true
}

// responseText accepts both the canonical Responses API string fields and the
// object-shaped text values returned by several Responses-compatible providers.
func responseText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var wrapped struct {
		Value string `json:"value"`
		Text  string `json:"text"`
	}
	if json.Unmarshal(raw, &wrapped) == nil {
		if wrapped.Value != "" {
			return wrapped.Value
		}
		return wrapped.Text
	}
	return ""
}
