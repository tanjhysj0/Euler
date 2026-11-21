import { Empty, Space, Tag } from 'antd'
import type { MessageView } from '../../api/types'
import MarkdownView from '../../components/MarkdownView'

interface MessageListProps {
  messages: MessageView[]
  deltas: Record<string, string>
  reasoning: Record<string, string>
}

// mergeStreamingText 合并权威 content 与本地增量：服务端先写库后发 delta，
// content 正常已包含全部增量文本。取 content 后缀与 delta 前缀的最长重叠去重；
// content 落后于本地增量时只去掉重叠部分，无重叠则全量保留，避免重复或丢字。
function mergeStreamingText(content: string, delta: string): string {
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

// MessageList 渲染消息 content（streaming 时拼接本地增量）与模型思考过程；
// reasoning 只来自实时事件，不落库，刷新后不展示。
export default function MessageList({ messages, deltas, reasoning }: MessageListProps) {
  if (messages.length === 0) {
    return <Empty description="暂无消息" />
  }
  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      {messages.map((message) => {
        const content =
          message.status === 'streaming'
            ? mergeStreamingText(message.content, deltas[message.id] ?? '')
            : message.content
        // 部分模型会返回空白 reasoning_content，去掉首尾空白后再判断，
        // 避免展示空的“思考过程”折叠区。
        const thinking = reasoning[message.id]?.trim()
        return (
          <div key={message.id}>
            <Tag>{message.role === 'user' ? '用户' : '助手'}</Tag>
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
    </Space>
  )
}
