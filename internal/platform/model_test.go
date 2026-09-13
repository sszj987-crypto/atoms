package platform

import (
	"encoding/json"
	"testing"
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

func TestUsableProjectTitle(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{"智能待办", true},
		{"需求管理", true},
		{"AI短片推荐", true},
		{"2048游戏", true},
		{"用户要求：为 Web 应用需求生成简洁具体的中文项目名称。需求：", false},
		{"用户想要一个简洁、具体的中文项目名称，用于一个 Web 应用（计", false},
		{"项目名称：智能待办", false},
		{"Web应用", false},
		{"这是一个超过十六个字符限制的项目名称示例", false},
	} {
		if got := usableProjectTitle(tc.title); got != tc.want {
			t.Fatalf("usableProjectTitle(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

func TestNormalizeProjectTitle(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  string
	}{
		{"项目名称：智能待办", "智能待办"},
		{"\"智能待办\"", "智能待办"},
		{"**智能待办**", "智能待办"},
		{`{"name":"智能待办"}`, "智能待办"},
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
		{"normalizes label", `{"output_text":"项目名称：智能待办"}`, "智能待办"},
		{"rejects explanation", `{"output_text":"用户想要一个简洁、具体的中文项目名称，用于一个 Web 应用（计"}`, ""},
		{"skips reasoning, extracts output_text", `{"output":[{"type":"reasoning","content":[{"type":"reasoning_text","text":"The user wants a name."}]},{"type":"message","content":[{"type":"output_text","text":"随手算"}]}]}`, "随手算"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectTitleFromResponse([]byte(tc.raw)); got != tc.want {
				t.Fatalf("projectTitleFromResponse() = %q, want %q", got, tc.want)
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
	valid := func(base, key, model string) bool { return validateModelInput(modelInput{BaseURL: base, APIKey: key, Model: model}) }
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
