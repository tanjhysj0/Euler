package tracing

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel/attribute"
)

const (
	toolCallType  = "function"
	emptyJSONList = "[]"
	emptyJSONText = `""`
	emptyValue    = ""
	noItems       = 0
)

type chatMessage struct {
	Role             string         `json:"role"`
	Content          string         `json:"content,omitempty"`
	Name             string         `json:"name,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// jsonText 把文本编码为 JSON 字符串；Langfuse 的 input/output 字段要求传 JSON 文本。
func jsonText(text string) string {
	data, err := json.Marshal(text)
	if err != nil {
		return emptyJSONText
	}
	return string(data)
}

// messagesJSON 把 Eino 消息转换为 OpenAI 兼容的消息数组，
// 使 Langfuse 能按对话渲染并展示工具调用卡片。
func messagesJSON(messages []*schema.Message) string {
	converted := make([]chatMessage, noItems, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		converted = append(converted, toChatMessage(message))
	}
	data, err := json.Marshal(converted)
	if err != nil {
		return emptyJSONList
	}
	return string(data)
}

func toChatMessage(message *schema.Message) chatMessage {
	converted := chatMessage{
		Role:             string(message.Role),
		Content:          message.Content,
		Name:             message.Name,
		ToolCallID:       message.ToolCallID,
		ReasoningContent: message.ReasoningContent,
	}
	for _, call := range message.ToolCalls {
		converted.ToolCalls = append(converted.ToolCalls, chatToolCall{
			ID:   call.ID,
			Type: toolCallTypeOf(call.Type),
			Function: chatFunction{
				Name: call.Function.Name, Arguments: call.Function.Arguments,
			},
		})
	}
	return converted
}

func toolCallTypeOf(callType string) string {
	if callType == emptyValue {
		return toolCallType
	}
	return callType
}

// generationOutputAttrs 写入模型输出；推理内容随输出消息一并保存。
func generationOutputAttrs(ctx context.Context, message *schema.Message) []attribute.KeyValue {
	if message == nil {
		return nil
	}
	return []attribute.KeyValue{attribute.String(
		attrObsOutput, redact(ctx, messagesJSON([]*schema.Message{message})),
	)}
}

// usageAttr 把 token 用量写入 Langfuse 的 usage_details。
func usageAttr(usage *model.TokenUsage, message *schema.Message) []attribute.KeyValue {
	details := usageMapOfModel(usage)
	if len(details) == noItems && message != nil && message.ResponseMeta != nil {
		details = usageMapOfSchema(message.ResponseMeta.Usage)
	}
	if len(details) == noItems {
		return nil
	}
	data, err := json.Marshal(details)
	if err != nil {
		return nil
	}
	return []attribute.KeyValue{attribute.String(attrObsUsage, string(data))}
}

func usageMapOfModel(usage *model.TokenUsage) map[string]int {
	if usage == nil {
		return nil
	}
	return usageMap(usageNumbers{
		prompt:     usage.PromptTokens,
		completion: usage.CompletionTokens,
		total:      usage.TotalTokens,
		cached:     usage.PromptTokenDetails.CachedTokens,
		reasoning:  usage.CompletionTokensDetails.ReasoningTokens,
	})
}

func usageMapOfSchema(usage *schema.TokenUsage) map[string]int {
	if usage == nil {
		return nil
	}
	return usageMap(usageNumbers{
		prompt:     usage.PromptTokens,
		completion: usage.CompletionTokens,
		total:      usage.TotalTokens,
		cached:     usage.PromptTokenDetails.CachedTokens,
		reasoning:  usage.CompletionTokensDetails.ReasoningTokens,
	})
}

// usageNumbers 汇总一次模型调用的各类 token 数量。
type usageNumbers struct {
	prompt     int
	completion int
	total      int
	cached     int
	reasoning  int
}

func usageMap(numbers usageNumbers) map[string]int {
	details := map[string]int{}
	input := numbers.prompt
	if numbers.cached > noItems && numbers.cached <= input {
		// 缓存命中已包含在 prompt tokens 中，单独记入缓存类型以免重复计数
		input -= numbers.cached
	}
	if input > noItems {
		details["input"] = input
	}
	if numbers.completion > noItems {
		details["output"] = numbers.completion
	}
	if numbers.total > noItems {
		details["total"] = numbers.total
	}
	if numbers.cached > noItems {
		details["cache_read_input_tokens"] = numbers.cached
	}
	if numbers.reasoning > noItems {
		details["reasoning"] = numbers.reasoning
	}
	return details
}

// modelParamsJSON 序列化模型调用参数，便于在 Langfuse 中对比不同配置。
func modelParamsJSON(cfg *model.Config) string {
	params := map[string]any{}
	if cfg.MaxTokens > noItems {
		params["max_tokens"] = cfg.MaxTokens
	}
	if cfg.Temperature != noItems {
		params["temperature"] = cfg.Temperature
	}
	if cfg.TopP != noItems {
		params["top_p"] = cfg.TopP
	}
	if len(cfg.Stop) > noItems {
		params["stop"] = cfg.Stop
	}
	if len(params) == noItems {
		return emptyValue
	}
	data, err := json.Marshal(params)
	if err != nil {
		return emptyValue
	}
	return string(data)
}

// completionStartAttr 记录非流式模型开始生成的时间。
func completionStartAttr(state *spanState) attribute.KeyValue {
	return completionStartAt(state, state.started)
}

// completionStartAt 记录模型开始生成的时间；at 为零值时回退到请求开始时间。
// 流式调用传入首个有效分片时间，才能让 Langfuse 计算出真实的 TTFT。
func completionStartAt(state *spanState, at time.Time) attribute.KeyValue {
	if at.IsZero() {
		at = state.started
	}
	return attribute.String(
		attrObsCompletion, at.UTC().Format(time.RFC3339Nano),
	)
}

// toolOutputText 返回工具输出的原始文本；结构化输出序列化为 JSON。
func toolOutputText(out *tool.CallbackOutput) string {
	if out.ToolOutput != nil {
		if data, err := json.Marshal(out.ToolOutput); err == nil {
			return string(data)
		}
	}
	return out.Response
}

// jsonOrText 保留合法 JSON 原样，其余文本编码为 JSON 字符串，
// 避免工具输出被二次转义后在 Langfuse 中难以阅读。
func jsonOrText(text string) string {
	if json.Valid([]byte(text)) {
		return text
	}
	return jsonText(text)
}

func joinStrings(values []string) string {
	return strings.Join(values, "")
}
