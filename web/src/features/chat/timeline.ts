import type { RunEvent } from '../../api/types'

// TimelineEvent 是从 RunEvent 中裁剪出的、重建对话时间线所需的最小事件。
// 只保留序号、类型、消息增量或工具调用 ID，避免把工具结果等大字段留在内存。
export interface TimelineEvent {
  seq: number
  type: string
  messageId?: string
  delta?: string
  toolCallId?: string
}

// TimelineItem 是一次 Run 内按发生顺序排列的条目：
// 文本、思考过程与工具调用可以交错出现。
export type TimelineItem =
  | { kind: 'text'; key: string; messageId: string; content: string }
  | { kind: 'reasoning'; key: string; messageId: string; content: string }
  | { kind: 'tool'; key: string; toolCallId: string }

// RenderItem 是分组后的渲染条目：连续工具调用被合并为 tool-group，
// 其余文本与思考条目保持原样。
export type RenderItem = Exclude<TimelineItem, { kind: 'tool' }> | ToolGroupItem

// ToolGroupItem 表示一段连续的工具调用。一次模型输出可能连续调用多个工具，
// 收起时只隐藏这一段，而不是整条消息里的全部工具。
export interface ToolGroupItem {
  kind: 'tool-group'
  key: string
  toolCallIds: string[]
}

// groupToolItems 把连续的工具调用合并为一组；文本或思考会切断分组。
export function groupToolItems(items: TimelineItem[]): RenderItem[] {
  const grouped: RenderItem[] = []
  for (const item of items) {
    if (item.kind !== 'tool') {
      grouped.push(item)
      continue
    }
    const last = grouped[grouped.length - 1]
    if (last?.kind === 'tool-group') {
      last.toolCallIds.push(item.toolCallId)
      continue
    }
    grouped.push({
      kind: 'tool-group',
      key: `tool-group-${item.toolCallId}`,
      toolCallIds: [item.toolCallId],
    })
  }
  return grouped
}

function stringField(payload: Record<string, unknown>, key: string): string {
  const value = payload[key]
  return typeof value === 'string' ? value : ''
}

// mergeStreamingText 合并权威 content 与本地增量：服务端先写库后发 delta，
// content 正常已包含全部增量文本。取 content 后缀与 delta 前缀的最长重叠去重；
// content 落后于本地增量时只去掉重叠部分，无重叠则全量保留，避免重复或丢字。
export function mergeStreamingText(content: string, delta: string): string {
  if (delta === '') {
    return content
  }
  for (let k = Math.min(content.length, delta.length); k > 0; k -= 1) {
    if (content.endsWith(delta.slice(0, k))) {
      return content + delta.slice(k)
    }
  }
  return content + delta
}

// toTimelineEvent 从 SSE 事件里提取时间线需要的最小字段；无关事件返回 null。
export function toTimelineEvent(envelope: RunEvent): TimelineEvent | null {
  if (envelope.type === 'message.delta' || envelope.type === 'message.reasoning') {
    const messageId = stringField(envelope.payload, 'message_id')
    if (!messageId) {
      return null
    }
    return {
      seq: envelope.seq,
      type: envelope.type,
      messageId,
      delta: stringField(envelope.payload, 'delta'),
    }
  }
  if (envelope.type.startsWith('tool.')) {
    const toolCallId = stringField(envelope.payload, 'tool_call_id')
    if (!toolCallId) {
      return null
    }
    return { seq: envelope.seq, type: envelope.type, toolCallId }
  }
  return null
}

// buildRunTimeline 按事件顺序重建文本与工具的交错关系。
// 连续的同一条消息增量会合并为一个文本段，遇到工具调用后开启新的文本段。
export function buildRunTimeline(events: TimelineEvent[]): TimelineItem[] {
  const sorted = [...events].sort((left, right) => left.seq - right.seq)
  const items: TimelineItem[] = []
  const toolKeys = new Set<string>()

  for (const event of sorted) {
    if (
      (event.type === 'message.delta' || event.type === 'message.reasoning') &&
      event.messageId &&
      event.delta
    ) {
      const kind = event.type === 'message.delta' ? 'text' : 'reasoning'
      const last = items[items.length - 1]
      if (last?.kind === kind && last.messageId === event.messageId) {
        // 同一段内继续追加，保持为一个渲染块。
        last.content += event.delta
        continue
      }
      items.push({
        kind,
        key: `${kind}-${event.seq}`,
        messageId: event.messageId,
        content: event.delta,
      })
      continue
    }
    if (event.type === 'tool.started' && event.toolCallId && !toolKeys.has(event.toolCallId)) {
      toolKeys.add(event.toolCallId)
      items.push({
        kind: 'tool',
        key: `tool-${event.toolCallId}`,
        toolCallId: event.toolCallId,
      })
    }
  }
  return items
}
