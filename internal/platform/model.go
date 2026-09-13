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
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	errModelConfigRequired   = errors.New("model configuration is required")
	errModelTitleUnavailable = errors.New("model title is unavailable")
)

const maxProjectNameRunes = 16

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
	for attempt := 0; attempt < 2; attempt++ {
		raw, requestErr := s.requestProjectTitle(ctx, in, description, attempt > 0)
		if requestErr != nil {
			continue
		}
		if title := projectTitleFromResponse(raw); title != "" {
			return title, nil
		}
	}
	return "", errModelTitleUnavailable
}

func (s *modelService) requestProjectTitle(ctx context.Context, in modelInput, description string, retry bool) ([]byte, error) {
	instructions := "你是产品命名器。只返回最终项目名称，不得解释、复述需求或输出标签。"
	prompt := "为下面的 Web 应用取一个具体的中文产品名。名称建议 2–12 个字，最多 16 个字符；应体现应用用途。只输出一行名称本身，不要输出‘项目名称’、‘用户想要’、‘用于’等说明文字，不要引号、序号、冒号、括号或句号。\n\n<需求>\n" + strings.TrimSpace(description) + "\n</需求>"
	if retry {
		instructions = "上次返回的不是有效名称。现在只返回一个 2–12 字的中文产品名，禁止任何解释或提示词复述。"
		prompt = "请重新命名。仅输出名称本身。\n\n<需求>\n" + strings.TrimSpace(description) + "\n</需求>"
	}
	body, _ := json.Marshal(map[string]any{
		"model":             in.Model,
		"instructions":      instructions,
		"input":             prompt,
		"max_output_tokens": 32,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(in.BaseURL, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+in.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errModelTitleUnavailable
	}
	return io.ReadAll(io.LimitReader(res.Body, 64<<10))
}

func projectTitleFromResponse(raw []byte) string {
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
		return ""
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
	title = normalizeProjectTitle(title)
	if !usableProjectTitle(title) {
		return ""
	}
	return title
}

func normalizeProjectTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	if strings.HasPrefix(title, "{") {
		var wrapped struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(title), &wrapped) == nil && strings.TrimSpace(wrapped.Name) != "" {
			title = wrapped.Name
		}
	}
	for _, line := range strings.Split(title, "\n") {
		if strings.TrimSpace(line) != "" {
			title = line
			break
		}
	}
	title = strings.TrimSpace(strings.Trim(title, " \t\"'`*_#“”‘’「」"))
	for _, prefix := range []string{"项目名称：", "项目名称:", "项目名：", "项目名:", "名称：", "名称:", "标题：", "标题:", "name:", "title:"} {
		if strings.HasPrefix(strings.ToLower(title), strings.ToLower(prefix)) {
			title = strings.TrimSpace(title[len(prefix):])
			break
		}
	}
	title = strings.Join(strings.Fields(title), " ")
	return strings.Trim(title, " \t\"'`*_#，,。.!！?？;；")
}

func usableProjectTitle(title string) bool {
	title = strings.TrimSpace(title)
	runes := []rune(title)
	if len(runes) < 2 || len(runes) > maxProjectNameRunes || strings.ContainsAny(title, ":：\n\r\t，,。.!！?？;；()（）[]【】{}<>《》") {
		return false
	}
	compact := strings.ToLower(strings.ReplaceAll(title, " ", ""))
	for _, phrase := range []string{"用户想要", "用户要求", "用户希望", "项目名称", "项目名为", "名称为", "标题为", "生成一个", "生成简洁", "简洁具体", "应用需求", "需求描述", "根据需求", "用于一个", "只输出", "不要解释", "为web应用"} {
		if strings.Contains(compact, phrase) {
			return false
		}
	}
	for _, generic := range []string{"项目", "新项目", "应用", "web应用", "网页应用", "待命名项目", "untitledproject"} {
		if compact == generic {
			return false
		}
	}
	for _, r := range runes {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func validProjectName(name string) bool {
	name = strings.TrimSpace(name)
	length := len([]rune(name))
	return length >= 1 && length <= maxProjectNameRunes && !strings.ContainsAny(name, "\n\r\t")
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
