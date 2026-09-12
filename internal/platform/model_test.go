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
		{"用户要求：为 Web 应用需求生成简洁具体的中文项目名称。需求：", false},
		{"项目名称：智能待办", false},
	} {
		if got := usableProjectTitle(tc.title); got != tc.want {
			t.Fatalf("usableProjectTitle(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}
