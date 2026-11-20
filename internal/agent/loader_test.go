package agent

import (
	"strings"
	"testing"
)

const testWorkspace = "/tmp/workspace"

func TestRenderSystemPromptDescribesMethodology(t *testing.T) {
	prompt, err := renderSystemPrompt("coding", testWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	mustContain := []string{
		"先读后写",
		"先复现",
		"找根因",
		"最小修复",
		"验证",
		testWorkspace,
	}
	for _, text := range mustContain {
		if !strings.Contains(prompt, text) {
			t.Fatalf("system prompt 缺少 %q: %s", text, prompt)
		}
	}
	if strings.Contains(prompt, "审批") {
		t.Fatalf("system prompt 不应描述审批机制: %s", prompt)
	}
}

func TestRenderSystemPromptFallsBackToCoding(t *testing.T) {
	prompt, err := renderSystemPrompt("", testWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "先读后写") {
		t.Fatalf("默认模板内容错误: %s", prompt)
	}
}

func TestRenderSystemPromptRejectsUnknownTemplate(t *testing.T) {
	if _, err := renderSystemPrompt("missing", testWorkspace); err == nil {
		t.Fatal("未知模板应当报错")
	}
}
