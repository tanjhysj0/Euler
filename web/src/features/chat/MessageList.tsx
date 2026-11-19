import { Empty, Space, Tag } from 'antd'
import type { MessageView } from '../../api/types'
import MarkdownView from '../../components/MarkdownView'

interface MessageListProps {
  messages: MessageView[]
  deltas: Record<string, string>
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

// MessageList 只渲染消息 content（streaming 时拼接本地增量），不读取 reasoning。
export default function MessageList({ messages, deltas }: MessageListProps) {
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
        return (
          <div key={message.id}>
            <Tag>{message.role === 'user' ? '用户' : '助手'}</Tag>
            <MarkdownView content={content} />
          </div>
        )
      })}
    </Space>
  )
}
