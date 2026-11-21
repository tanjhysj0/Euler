import { Button } from 'antd'
import { isValidElement } from 'react'
import type { ReactNode } from 'react'
import Markdown from 'react-markdown'
import rehypeHighlight from 'rehype-highlight'
import remarkGfm from 'remark-gfm'
import 'highlight.js/styles/github.css'
import TerminalBlock from './TerminalBlock'

// shellLanguages 是模型输出的命令类代码块语言标记，渲染为终端样式。
const shellLanguages = new Set([
  'bash',
  'sh',
  'shell',
  'zsh',
  'console',
  'terminal',
  'shellsession',
])

// nodeText 递归提取 ReactNode 的纯文本，用于复制代码块内容。
function nodeText(node: ReactNode): string {
  if (node === null || node === undefined || typeof node === 'boolean') {
    return ''
  }
  if (typeof node === 'string' || typeof node === 'number') {
    return String(node)
  }
  if (Array.isArray(node)) {
    return node.map(nodeText).join('')
  }
  if (isValidElement<{ children?: ReactNode }>(node)) {
    return nodeText(node.props.children)
  }
  return ''
}

// codeLanguage 从 pre 内的 code 元素 className（language-xxx）取语言标记。
function codeLanguage(children: ReactNode): string {
  const child = Array.isArray(children) ? children[0] : children
  if (!isValidElement<{ className?: string }>(child)) {
    return ''
  }
  const match = /language-([\w-]+)/.exec(child.props.className ?? '')
  return match?.[1]?.toLowerCase() ?? ''
}

function CodeBlock({ children }: { children?: ReactNode }) {
  const language = codeLanguage(children)
  if (shellLanguages.has(language)) {
    return (
      <TerminalBlock command={nodeText(children).replace(/\n$/, '')} title={language} />
    )
  }
  return (
    <div className="code-block">
      <Button size="small" onClick={() => void navigator.clipboard.writeText(nodeText(children))}>
        复制
      </Button>
      <pre>{children}</pre>
    </div>
  )
}

// MarkdownView 用与聊天消息一致的渲染器展示 Markdown（消息与 .md 产物共用）。
export default function MarkdownView({ content }: { content: string }) {
  return (
    <Markdown
      remarkPlugins={[remarkGfm]}
      rehypePlugins={[rehypeHighlight]}
      components={{ pre: CodeBlock }}
    >
      {content}
    </Markdown>
  )
}
