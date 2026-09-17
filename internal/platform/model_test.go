package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestResponseText(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"plain string", `"待办清单"`, "待办清单"},
		{"value object", `{"value":"待办清单"}`, "待办清单"},
		{"text object", `{"text":"待办清单"}`, "待办清单"},
		{"empty", `null`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := responseText(json.RawMessage(tc.raw)); got != tc.want {
				t.Fatalf("responseText() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeProjectTitle(t *testing.T) {
	for _, tc := range []struct{ title, want string }{
		{`{"name":"智能待办"}`, "智能待办"},
		{`{"name":" Task Flow "}`, "Task Flow"},
		{`{"name":"C++ Lab"}`, "C++ Lab"},
		{`{"name":"A"}`, "A"},
		{`{"name":null}`, ""},
		{`{"name":123}`, ""},
		{`{"title":"智能待办"}`, ""},
		{`{"name":"智能待办","explanation":"待办工具"}`, ""},
		{"项目名称：智能待办", ""},
		{"**智能待办**", ""},
	} {
		if got := normalizeProjectTitle(tc.title); got != tc.want {
			t.Fatalf("normalizeProjectTitle(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}

func TestProjectTitleFromResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"rejects plain label", `{"output_text":"项目名称：智能待办"}`, ""},
		{"rejects explanation", `{"output_text":"用户想要一个简洁、具体的中文项目名称，用于一个 Web 应用（计"}`, ""},
		{"skips reasoning, extracts output_text", `{"output":[{"type":"reasoning","content":[{"type":"reasoning_text","text":"{\"name\":\"The user wants a name.\"}"}]},{"type":"message","content":[{"type":"output_text","text":"{\"name\":\"随手算\"}"}]}]}`, "随手算"},
		{"English name with punctuation", `{"output_text":"{\"name\":\"C++ Lab\"}"}`, "C++ Lab"},
		{"single character name", `{"output_text":"{\"name\":\"A\"}"}`, "A"},
		{"name containing newline", `{"output_text":"{\"name\":\"Task\\nFlow\"}"}`, ""},
		{"structured name", `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"{\"name\":\"智能待办\"}"}]}]}`, "智能待办"},
		{"object text", `{"output":[{"type":"message","content":[{"type":"output_text","text":{"value":"{\"name\":\"智能待办\"}"}}]}]}`, "智能待办"},
		{"chat compatible", `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"name\":\"智能待办\"}","refusal":null}}]}`, "智能待办"},
		{"reasoning item with misleading text type", `{"output":[{"type":"reasoning","content":[{"type":"output_text","text":"{\"name\":\"智能待办\"}"}]}]}`, ""},
		{"reasoning only ignores aggregate", `{"output_text":"{\"name\":\"智能待办\"}","output":[{"type":"reasoning","content":[{"type":"reasoning_text","text":"{\"name\":\"智能待办\"}"}]}]}`, ""},
		{"skips commentary", `{"output":[{"type":"message","phase":"commentary","content":[{"type":"output_text","text":"{\"name\":\"开始命名\"}"}]},{"type":"message","phase":"final_answer","content":[{"type":"output_text","text":"{\"name\":\"智能待办\"}"}]}]}`, "智能待办"},
		{"rejects user message", `{"output":[{"type":"message","role":"user","content":[{"type":"output_text","text":"{\"name\":\"智能待办\"}"}]}]}`, ""},
		{"incomplete response", `{"status":"incomplete","output_text":"{\"name\":\"智能待办\"}"}`, ""},
		{"failed response", `{"status":"failed","output_text":"{\"name\":\"智能待办\"}"}`, ""},
		{"error envelope", `{"error":{"message":"unavailable"},"output_text":"{\"name\":\"智能待办\"}"}`, ""},
		{"incomplete details", `{"incomplete_details":{"reason":"max_output_tokens"},"output_text":"{\"name\":\"智能待办\"}"}`, ""},
		{"incomplete message", `{"output":[{"type":"message","status":"incomplete","content":[{"type":"output_text","text":"{\"name\":\"智能待办\"}"}]}]}`, ""},
		{"refusal", `{"output":[{"type":"message","content":[{"type":"refusal","refusal":"抱歉"},{"type":"output_text","text":"{\"name\":\"智能待办\"}"}]}]}`, ""},
		{"truncated chat response", `{"choices":[{"finish_reason":"length","message":{"content":"{\"name\":\"智能待办\"}"}}]}`, ""},
		{"chat refusal", `{"choices":[{"message":{"content":"{\"name\":\"智能待办\"}","refusal":"抱歉"}}]}`, ""},
		{"multiline explanation", `{"output_text":"智能待办\n这是一个待办工具"}`, ""},
		{"multiple text blocks", `{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"name\":\"智能待办\"}"},{"type":"output_text","text":"这是一个待办工具"}]}]}`, ""},
		{"malformed name JSON", `{"output_text":"{\"name\":\"智能待办\""}`, ""},
		{"JSON with explanation", `{"output_text":"{\"name\":\"智能待办\",\"explanation\":\"待办工具\"}"}`, ""},
		{"empty", `{}`, ""},
		{"non JSON", `upstream unavailable`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectTitleFromResponse([]byte(tc.raw)); got != tc.want {
				t.Fatalf("projectTitleFromResponse() = %q, want %q", got, tc.want)
			}
		})
	}
}

type titleTransport func(*http.Request) (*http.Response, error)

func (f titleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGenerateProjectTitle(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		requestErr error
		want       string
	}{
		{"English name", 200, `{"output_text":"{\"name\":\"Task Flow\"}"}`, nil, "Task Flow"},
		{"overlong name falls back", 200, `{"output_text":"{\"name\":\"abcdefghijklmnopq\"}"}`, nil, defaultProjectName},
		{"empty name falls back", 200, `{"output_text":"{\"name\":\"\"}"}`, nil, defaultProjectName},
		{"schema unsupported falls back", 400, `{"error":{"param":"text.format","message":"Unsupported json_schema"}}`, nil, defaultProjectName},
		{"valid name", 200, `{"output_text":"{\"name\":\"智能待办\"}"}`, nil, "智能待办"},
		{"invalid answer falls back", 200, `{"output_text":"用户想要待办工具"}`, nil, defaultProjectName},
		{"reasoning only falls back", 200, `{"output":[{"type":"reasoning"}]}`, nil, defaultProjectName},
		{"truncation falls back", 200, `{"status":"incomplete","output_text":"智能待办"}`, nil, defaultProjectName},
		{"malformed response falls back", 200, `not JSON`, nil, defaultProjectName},
		{"unauthorized falls back without retry", 401, `{"error":{"message":"invalid key"}}`, nil, defaultProjectName},
		{"unrelated parameter error does not retry", 400, `{"error":{"param":"model","message":"Unsupported model"}}`, nil, defaultProjectName},
		{"service failure falls back without retry", 503, `unavailable`, nil, defaultProjectName},
		{"network failure falls back", 0, "", errors.New("connection failed"), defaultProjectName},
		{"timeout falls back", 0, "", context.DeadlineExceeded, defaultProjectName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			ctx := context.Background()
			service := &modelService{client: &http.Client{Transport: titleTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("incorrect title request: %s %s", r.Method, r.URL.Path)
				}
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > projectTitleTimeout {
					t.Error("title request must have a bounded deadline")
				}
				var payload struct {
					Model           string `json:"model"`
					Instructions    string `json:"instructions"`
					Input           string `json:"input"`
					MaxOutputTokens int    `json:"max_output_tokens"`
					Reasoning       struct {
						Effort string `json:"effort"`
					} `json:"reasoning"`
					Stream *bool `json:"stream"`
					Store  *bool `json:"store"`
					Text   struct {
						Format struct {
							Type   string `json:"type"`
							Name   string `json:"name"`
							Strict bool   `json:"strict"`
							Schema struct {
								Type       string `json:"type"`
								Properties map[string]struct {
									Type string `json:"type"`
								} `json:"properties"`
								Required             []string `json:"required"`
								AdditionalProperties bool     `json:"additionalProperties"`
							} `json:"schema"`
						} `json:"format"`
					} `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload.Model != "test-model" || payload.Reasoning.Effort != "none" || payload.MaxOutputTokens != 128 || payload.Stream == nil || *payload.Stream || payload.Store == nil || *payload.Store || strings.Contains(payload.Instructions, "JSON") || strings.Contains(payload.Instructions, `{"name"`) {
					t.Errorf("incorrect naming parameters: %+v", payload)
				}
				format := payload.Text.Format
				if format.Type != "json_schema" || format.Name != "project_title" || !format.Strict || format.Schema.Type != "object" || len(format.Schema.Properties) != 1 || format.Schema.Properties["name"].Type != "string" || len(format.Schema.Required) != 1 || format.Schema.Required[0] != "name" || format.Schema.AdditionalProperties {
					t.Errorf("incorrect structured output format: %+v", format)
				}
				var input map[string]string
				if json.Unmarshal([]byte(payload.Input), &input) != nil || input["description"] != "开发一个待办工具" {
					t.Error("description should be encoded separately from instructions")
				}
				if tc.requestErr != nil {
					return nil, tc.requestErr
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}}
			got := service.generateProjectTitle(ctx, modelInput{BaseURL: "https://example.com/v1/", APIKey: "test-key", Model: "test-model"}, " 开发一个待办工具 ")
			if got != tc.want || calls != 1 {
				t.Fatalf("generateProjectTitle() = %q, calls = %d; want %q, 1", got, calls, tc.want)
			}
			if ctx.Err() != nil {
				t.Fatal("naming must not cancel the creation context")
			}
		})
	}
}

func TestGenerateProjectTitleTimeoutKeepsCreationContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := &modelService{client: &http.Client{
		Timeout: 10 * time.Millisecond,
		Transport: titleTransport(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}),
	}}
	if got := service.generateProjectTitle(ctx, modelInput{BaseURL: "https://example.com/v1"}, "待办工具"); got != defaultProjectName {
		t.Fatalf("timed out naming returned %q", got)
	}
	if ctx.Err() != nil {
		t.Fatal("title timeout must leave the project creation context usable")
	}
}

func TestGenerateProjectTitleReasoningCompatibility(t *testing.T) {
	for _, tc := range []struct{ name, secondBody, want string }{
		{"compatible retry succeeds", `{"output_text":"{\"name\":\"智能待办\"}"}`, "智能待办"},
		{"invalid retry falls back", `{"output_text":"请提供更多信息"}`, defaultProjectName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var deadline time.Time
			service := &modelService{client: &http.Client{Transport: titleTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				currentDeadline, _ := r.Context().Deadline()
				status, body := 200, tc.secondBody
				if calls == 1 {
					deadline = currentDeadline
					if !nonNullJSON(payload["reasoning"]) {
						t.Error("first attempt must disable reasoning")
					}
					status, body = 400, `{"error":{"param":"reasoning.effort","message":"Unsupported value: none"}}`
				} else {
					if _, ok := payload["reasoning"]; ok {
						t.Error("compatibility retry must omit reasoning")
					}
					if !currentDeadline.Equal(deadline) {
						t.Error("retry must share the original deadline")
					}
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}}
			if got := service.generateProjectTitle(context.Background(), modelInput{BaseURL: "https://example.com/v1", Model: "test-model"}, "待办工具"); got != tc.want || calls != 2 {
				t.Fatalf("generateProjectTitle() = %q, calls = %d; want %q, 2", got, calls, tc.want)
			}
		})
	}
}

func TestValidProjectName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"需求管理", true},
		{"Task Flow", true},
		{"C++ Lab", true},
		{"用户想要", true},
		{"项目名称", true},
		{"abcdefghijklmnop", true},
		{"abcdefghijklmnopq", false},
		{"一二三四五六七八九十一二三四五六", true},
		{"一二三四五六七八九十一二三四五六七", false},
		{" ", false},
		{"第一行\r第二行", false},
		{"Task\tFlow", false},
		{"A", true},
		{"", false},
		{"这是一个超过十六个字符限制的项目名称示例", false},
		{"第一行\n第二行", false},
	} {
		if got := validProjectName(tc.name); got != tc.want {
			t.Fatalf("validProjectName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestValidateModelInput(t *testing.T) {
	valid := func(base, key, model string) bool {
		return validateModelInput(modelInput{BaseURL: base, APIKey: key, Model: model})
	}
	cases := []struct {
		name string
		got  bool
		want bool
	}{
		{"https remote", valid("https://api.example.com/v1", "sk-x", "m"), true},
		{"http remote rejected", valid("http://api.example.com/v1", "sk-x", "m"), false},
		{"http localhost allowed", valid("http://localhost:3000/v1", "sk-x", "m"), true},
		{"http 127.0.0.1 allowed", valid("http://127.0.0.1:3000/v1", "sk-x", "m"), true},
		{"missing key", valid("https://api.example.com/v1", "", "m"), false},
		{"missing model", valid("https://api.example.com/v1", "sk-x", ""), false},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Fatalf("%s: validateModelInput = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}
