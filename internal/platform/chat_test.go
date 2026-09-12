package platform

import "testing"

func TestExtractSummary(t *testing.T) {
	raw := []byte(`{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"先看看结构。"}}
{"type":"item.completed","item":{"id":"i2","type":"command_execution","command":"ls"}}
{"type":"item.completed","item":{"id":"i3","type":"agent_message","text":"最终总结：完成计算器。"}}
{"type":"turn.completed"}`)
	if got, want := extractSummary(raw), "最终总结：完成计算器。"; got != want {
		t.Fatalf("extractSummary = %q, want %q", got, want)
	}
	if got := extractSummary([]byte(`{"type":"turn.completed"}`)); got != "" {
		t.Fatalf("extractSummary without agent_message = %q, want empty", got)
	}
}
