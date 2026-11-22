package persistence

import (
	"encoding/json"

	"gorm.io/datatypes"
)

// 消息片段类型：parts 按发生顺序保存助手在一次 Run 中的完整过程。
const (
	emptyPartsJSON = 0
	minMergeParts  = 2
	lastPartOffset = 1
	firstPartIndex = 0

	PartTypeText      = "text"
	PartTypeReasoning = "reasoning"
	PartTypeTool      = "tool"
)

// Part 是 messages.parts 中的一个有序片段。工具片段只保存 tool_calls 的
// 标识，工具名称、参数与结果仍以 tool_calls 表为权威来源。
type Part struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// MarshalParts 把片段列表编码为 jsonb；nil 与空列表统一存为 []。
func MarshalParts(parts []Part) datatypes.JSON {
	encoded := parts
	if encoded == nil {
		encoded = []Part{}
	}
	data, err := json.Marshal(encoded)
	if err != nil {
		return datatypes.JSON([]byte("[]"))
	}
	return datatypes.JSON(data)
}

// ParseParts 解码片段列表；损坏或空值返回空列表，读取方回退到 content。
func ParseParts(raw datatypes.JSON) []Part {
	if len(raw) == emptyPartsJSON {
		return nil
	}
	var parts []Part
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	return parts
}

// TextParts 构造只含一段正文的片段列表，用于用户消息与缺失 parts 的旧数据。
func TextParts(text string) []Part {
	if text == "" {
		return []Part{}
	}
	return []Part{{Type: PartTypeText, Text: text}}
}

// CoalesceParts 合并相邻的同类文本片段。历史回填按事件逐条生成片段，
// 同一次连续输出可能被拆成多段；合并后渲染为单个文本块或思考块。
// 工具片段是顺序边界，不会被跨过合并。
func CoalesceParts(parts []Part) []Part {
	if len(parts) < minMergeParts {
		return parts
	}
	merged := make([]Part, firstPartIndex, len(parts))
	for _, part := range parts {
		last := len(merged) - lastPartOffset
		if last >= firstPartIndex && mergable(part.Type) && merged[last].Type == part.Type {
			merged[last].Text += part.Text
			continue
		}
		merged = append(merged, part)
	}
	return merged
}

// mergable 报告该片段类型是否可以把相邻同类型内容拼接起来。
func mergable(partType string) bool {
	return partType == PartTypeText || partType == PartTypeReasoning
}
