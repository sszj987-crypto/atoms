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

func TestCodexProgressCommandLifecycle(t *testing.T) {
	started := []byte(`{"type":"item.started","item":{"id":"item_2","type":"command_execution","command":"/bin/sh -lc 'cd /workspace && pnpm test'","status":"in_progress"}}`)
	event, ok := codexProgress(started)
	if !ok {
		t.Fatal("codexProgress did not recognize command start")
	}
	if event.StepID != "item_2" || event.Kind != "command" || event.Title != "运行自动化测试" || event.Status != "running" {
		t.Fatalf("unexpected command start: %#v", event)
	}
	if event.Detail != "cd . && pnpm test" {
		t.Fatalf("command detail = %q", event.Detail)
	}

	completed := []byte(`{"type":"item.completed","item":{"id":"item_2","type":"command_execution","command":"pnpm test","aggregated_output":"tests failed\nExpected 2, received 3","exit_code":1,"status":"failed"}}`)
	event, ok = codexProgress(completed)
	if !ok || event.Status != "failed" {
		t.Fatalf("unexpected command completion: %#v, %v", event, ok)
	}
	if event.Detail != "pnpm test · 退出码 1 · 错误摘要：Expected 2, received 3" {
		t.Fatalf("failure detail = %q", event.Detail)
	}
}

func TestCodexProgressFileChange(t *testing.T) {
	raw := []byte(`{"type":"item.completed","item":{"id":"item_10","type":"file_change","changes":[{"path":"/workspace/app/page.tsx","kind":"update"},{"path":"/workspace/app/new.tsx","kind":"add"}],"status":"completed"}}`)
	event, ok := codexProgress(raw)
	if !ok {
		t.Fatal("codexProgress did not recognize file change")
	}
	if event.Title != "更新 2 个项目文件" || event.Detail != "修改 app/page.tsx · 新增 app/new.tsx" || event.Status != "completed" {
		t.Fatalf("unexpected file event: %#v", event)
	}
}

func TestCodexProgressKeepsOnlyPublicAgentMessage(t *testing.T) {
	raw := []byte(`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Codex 正在检查项目结构。"}}`)
	event, ok := codexProgress(raw)
	if !ok || event.Title != "系统 正在检查项目结构。" {
		t.Fatalf("unexpected public message: %#v, %v", event, ok)
	}
	reasoning := []byte(`{"type":"item.completed","item":{"id":"item_r","type":"reasoning","text":"private reasoning detail"}}`)
	event, ok = codexProgress(reasoning)
	if !ok || event.Title != "分析实现方案" || event.Detail != "" {
		t.Fatalf("reasoning detail should not be exposed: %#v, %v", event, ok)
	}
}

func TestCodexProgressFiltersFallbackMetadataNotice(t *testing.T) {
	fallback := []byte(`{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Model metadata for ` + "`deepseek-flash`" + ` not found. Defaulting to fallback metadata; this can degrade performance and cause issues."}}`)
	if event, ok := codexProgress(fallback); ok {
		t.Fatalf("fallback metadata notice should be hidden, got %#v", event)
	}

	realError := []byte(`{"type":"item.completed","item":{"id":"item_1","type":"error","message":"Model request failed."}}`)
	event, ok := codexProgress(realError)
	if !ok || event.Kind != "error" || event.Status != "failed" || event.Title != "Model request failed." {
		t.Fatalf("real model error should remain visible, got %#v, %v", event, ok)
	}
}

func TestSafeCommandDetailRedactsSensitiveCommands(t *testing.T) {
	if got := safeCommandDetail(`env OPENAI_API_KEY=sk-secret pnpm test`); got != "受保护的项目命令" {
		t.Fatalf("safeCommandDetail = %q", got)
	}
	if got := safeCommandDetail("cat > /workspace/app/page.tsx <<'EOF'\nsecret source\nEOF"); got != "cat > app/page.tsx << …" {
		t.Fatalf("heredoc detail = %q", got)
	}
	if got := safeCommandOutput("request failed\nprovider-key-value", "provider-key-value"); got != "错误摘要：request failed" {
		t.Fatalf("sensitive output was not skipped: %q", got)
	}
}
