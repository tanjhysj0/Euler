package agent

import (
	"strings"
	"testing"

	"euler/internal/persistence"
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
		"运行平台：" + platformLabel(),
		"命令解释器：" + shellLabel(),
	}
	for _, text := range mustContain {
		if !strings.Contains(prompt, text) {
			t.Fatalf("system prompt 缺少 %q: %s", text, prompt)
		}
	}
	for _, placeholder := range []string{"{{workspace}}", "{{platform}}", "{{shell}}"} {
		if strings.Contains(prompt, placeholder) {
			t.Fatalf("system prompt 仍有未替换的占位符 %q: %s", placeholder, prompt)
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

func TestDisplayMessageSkipsEmptyAssistant(t *testing.T) {
	got := displayMessage(persistence.Message{Role: roleAssistant, Content: " \n"})
	if got != nil {
		t.Fatalf("空白助手消息应被跳过，实际得到 %#v", got)
	}
	assistant := displayMessage(persistence.Message{Role: roleAssistant, Content: "完成"})
	if assistant == nil || assistant.Content != "完成" {
		t.Fatalf("有效助手消息不应被丢弃: %#v", assistant)
	}
	if user := displayMessage(persistence.Message{Role: roleUser, Content: "hi"}); user == nil {
		t.Fatal("用户消息不应被丢弃")
	}
}
