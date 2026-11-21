import { Button } from 'antd'

interface TerminalBlockProps {
  // command 为要展示的命令；多行时首行带提示符。
  command?: string
  // output 为命令输出，保留原始换行与空白。
  output?: string
  exitCode?: number
  timedOut?: boolean
  truncated?: boolean
  title?: string
}

// TerminalBlock 以终端样式展示命令与输出：深色背景、提示符、状态与复制。
// 正文 Markdown 的 shell 代码块和 shell 工具结果共用这一视觉语言。
export default function TerminalBlock({
  command,
  output,
  exitCode,
  timedOut,
  truncated,
  title = '终端',
}: TerminalBlockProps) {
  const commandLines = command ? command.split('\n') : []
  const copyText = [command, output].filter(Boolean).join('\n')
  return (
    <div className="terminal-block">
      <div className="terminal-block-bar">
        <span>{title}</span>
        <span className="terminal-block-meta">
          {timedOut && <span className="terminal-block-bad">已超时</span>}
          {truncated && <span className="terminal-block-warn">输出已截断</span>}
          {exitCode !== undefined && (
            <span className={exitCode === 0 ? 'terminal-block-ok' : 'terminal-block-bad'}>
              退出码 {exitCode}
            </span>
          )}
          <Button size="small" onClick={() => void navigator.clipboard.writeText(copyText)}>
            复制
          </Button>
        </span>
      </div>
      <pre className="terminal-block-body">
        {commandLines.map((line, index) => (
          <div key={`command-${index}`}>
            {index === 0 && <span className="terminal-prompt">$ </span>}
            {line}
          </div>
        ))}
        {output && <div className="terminal-output">{output}</div>}
      </pre>
    </div>
  )
}
