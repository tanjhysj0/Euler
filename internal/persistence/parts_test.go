package persistence

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCoalescePartsMergesAdjacentTextAndReasoning 验证历史回填产生的连续
// 同类片段会合并，工具片段作为顺序边界保留。
func TestCoalescePartsMergesAdjacentTextAndReasoning(t *testing.T) {
	parts := []Part{
		{Type: PartTypeReasoning, Text: "工作区只有"},
		{Type: PartTypeReasoning, Text: "两个文件"},
		{Type: PartTypeText, Text: "我先看"},
		{Type: PartTypeTool, ToolCallID: "tc1"},
		{Type: PartTypeText, Text: "再看"},
		{Type: PartTypeText, Text: "文件"},
	}
	require.Equal(t, []Part{
		{Type: PartTypeReasoning, Text: "工作区只有两个文件"},
		{Type: PartTypeText, Text: "我先看"},
		{Type: PartTypeTool, ToolCallID: "tc1"},
		{Type: PartTypeText, Text: "再看文件"},
	}, CoalesceParts(parts))
}

// TestCoalescePartsKeepsShortInput 验证单片段与纯工具片段原样返回。
func TestCoalescePartsKeepsShortInput(t *testing.T) {
	single := []Part{{Type: PartTypeText, Text: "回答"}}
	require.Equal(t, single, CoalesceParts(single))
	tool := []Part{{Type: PartTypeTool, ToolCallID: "tc1"}}
	require.Equal(t, tool, CoalesceParts(tool))
}
