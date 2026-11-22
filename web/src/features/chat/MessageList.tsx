import { Empty } from 'antd'
import { Fragment } from 'react'
import type { ReactNode } from 'react'
import type { MessageView } from '../../api/types'
import MarkdownView from '../../components/MarkdownView'
import { mergeStreamingText } from './timeline'

interface MessageListProps {
  messages: MessageView[]
  deltas: Record<string, string>
  reasoning: Record<string, string>
  // renderAssistant 允许调用方按 Run 事件顺序完整接管助手消息渲染；
  // 返回 undefined 时回退到默认的文本渲染。
  renderAssistant?: (message: MessageView) => ReactNode
}

// MessageList 渲染消息 content（streaming 时拼接本地增量）与模型思考过程；
// 角色标签与消息块类名供主题 CSS 区分用户/助手样式。
export default function MessageList({
  messages,
  deltas,
  reasoning,
  renderAssistant,
}: MessageListProps) {
  if (messages.length === 0) {
    return <Empty description="暂无消息" />
  }
  return (
    <div className="message-list">
      {messages.map((message) => {
        const custom = message.role !== 'user' ? renderAssistant?.(message) : undefined
        if (custom !== undefined) {
          return <Fragment key={message.id}>{custom}</Fragment>
        }
        const content =
          message.status === 'streaming'
            ? mergeStreamingText(message.content, deltas[message.id] ?? '')
            : message.content
        // 部分模型会返回空白 reasoning_content，去掉首尾空白后再判断，
        // 避免展示空的“思考过程”折叠区。
        const thinking = reasoning[message.id]?.trim()
        return (
          <div
            key={message.id}
            className={`message-item message-${message.role}`}
          >
            <span className="role-label">
              {message.role === 'user' ? '用户' : '助手'}
            </span>
            {message.role !== 'user' && thinking && (
              <details className="reasoning-block">
                <summary>{message.status === 'streaming' ? '思考中…' : '思考过程'}</summary>
                <div className="reasoning-text">{thinking}</div>
              </details>
            )}
            <MarkdownView content={content} />
          </div>
        )
      })}
    </div>
  )
}
