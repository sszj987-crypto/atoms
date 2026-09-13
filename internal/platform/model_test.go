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
