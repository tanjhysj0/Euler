import { Button } from 'antd'
import { useMemo, useState } from 'react'
import type { MessageView } from '../../api/types'
import MarkdownView from '../../components/MarkdownView'
import { ToolRow } from './ToolRow'

interface PartsTimelineProps {
  message: MessageView
}

// PartsTimeline 按落库片段还原已完成 Run 的真实对话顺序：思考、文本与
// 工具调用可以交错出现。工具详情来自消息携带的 tool_calls，不再逐 Run 拉快照。
export default function PartsTimeline({ message }: PartsTimelineProps) {
  const [collapsed, setCollapsed] = useState(false)
  const parts = message.parts ?? []
  const tools = useMemo(
    () => new Map((message.tool_calls ?? []).map((tool) => [tool.id, tool])),
    [message.tool_calls],
  )
  const toolCount = parts.filter((part) => part.type === 'tool').length

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

  return (
    <div className="message-item message-assistant">
      <span className="role-label">助手</span>
      <div className="run-timeline">
        {toolCount > 0 && (
          <Button
            size="small"
            className="timeline-toggle"
            onClick={() => setCollapsed((value) => !value)}
          >
            {collapsed ? `展开工具调用（${toolCount}）` : `收起工具调用（${toolCount}）`}
          </Button>
        )}
        {parts.map((part, index) => {
          if (part.type === 'reasoning') {
            return (
              <details className="reasoning-block" key={`reasoning-${index}`}>
                <summary>思考过程</summary>
                <div className="reasoning-text">{part.text}</div>
              </details>
            )
          }
          if (part.type === 'text') {
            return <MarkdownView key={`text-${index}`} content={part.text} />
          }
          if (collapsed) {
            return null
          }
          const tool = tools.get(part.tool_call_id)
          return tool ? <ToolRow key={`tool-${part.tool_call_id}`} tool={tool} /> : null
        })}
      </div>
    </div>
  )
}
