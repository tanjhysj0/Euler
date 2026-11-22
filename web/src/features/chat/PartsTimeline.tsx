import { Button } from 'antd'
import { useMemo, useState } from 'react'
import type { MessageView } from '../../api/types'
import MarkdownView from '../../components/MarkdownView'
import { ToolRow } from './ToolRow'
import { groupToolItems } from './timeline'
import type { RenderItem, TimelineItem } from './timeline'

interface PartsTimelineProps {
  message: MessageView
}

// PartsTimeline 按落库片段还原已完成 Run 的真实对话顺序：思考、文本与
// 工具调用可以交错出现。连续的工具调用自成一组，可单独收起。
// 工具详情来自消息携带的 tool_calls，不再逐 Run 拉快照。
export default function PartsTimeline({ message }: PartsTimelineProps) {
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(() => new Set())
  const parts = useMemo(() => message.parts ?? [], [message.parts])
  const tools = useMemo(
    () => new Map((message.tool_calls ?? []).map((tool) => [tool.id, tool])),
    [message.tool_calls],
  )
  const items = useMemo<RenderItem[]>(() => {
    const mapped: TimelineItem[] = parts.map((part, index) => {
      if (part.type === 'reasoning') {
        return {
          kind: 'reasoning',
          key: `reasoning-${index}`,
          messageId: message.id,
          content: part.text,
        }
      }
      if (part.type === 'text') {
        return {
          kind: 'text',
          key: `text-${index}`,
          messageId: message.id,
          content: part.text,
        }
      }
      return { kind: 'tool', key: `tool-${part.tool_call_id}`, toolCallId: part.tool_call_id }
    })
    return groupToolItems(mapped)
  }, [parts, message.id])

  if (parts.length === 0) {
    // 旧数据或异常情况下回退到 content，避免历史对话空白。
    if (message.content === '') {
      return null
    }
    return (
      <div className="message-item message-assistant">
        <span className="role-label">助手</span>
        <MarkdownView content={message.content} />
      </div>
    )
  }

  const toggleGroup = (key: string) => {
    setCollapsedGroups((previous) => {
      const next = new Set(previous)
      if (next.has(key)) {
        next.delete(key)
      } else {
        next.add(key)
      }
      return next
    })
  }

  return (
    <div className="message-item message-assistant">
      <span className="role-label">助手</span>
      <div className="run-timeline">
        {items.map((item) => {
          if (item.kind === 'text') {
            return <MarkdownView key={item.key} content={item.content} />
          }
          if (item.kind === 'reasoning') {
            return (
              <details className="reasoning-block" key={item.key}>
                <summary>思考过程</summary>
                <div className="reasoning-text">{item.content}</div>
              </details>
            )
          }
          const collapsed = collapsedGroups.has(item.key)
          return (
            <div className="tool-group" key={item.key}>
              <Button
                size="small"
                className="timeline-toggle"
                onClick={() => toggleGroup(item.key)}
              >
                {collapsed
                  ? `展开工具调用（${item.toolCallIds.length}）`
                  : `收起工具调用（${item.toolCallIds.length}）`}
              </Button>
              {!collapsed &&
                item.toolCallIds.map((toolCallId) => {
                  const tool = tools.get(toolCallId)
                  return tool ? <ToolRow key={toolCallId} tool={tool} /> : null
                })}
            </div>
          )
        })}
      </div>
    </div>
  )
}
