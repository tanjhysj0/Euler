import { Button } from 'antd'
import { useState } from 'react'
import type { ReactNode } from 'react'
import type { RunSnapshot, ToolCallStatus, ToolCallView } from '../../api/types'
import { useRunStore } from '../../stores/run'
import TerminalBlock from '../../components/TerminalBlock'
import ApprovalCard from './ApprovalCard'

interface ToolTimelineProps {
  snapshot: RunSnapshot | undefined
  onDecide: (approvalId: string, approved: boolean, reason: string) => Promise<void>
}

const statusLabels: Record<ToolCallStatus, { label: string; tone: string }> = {
  pending: { label: '等待中', tone: 'default' },
  waiting_approval: { label: '等待审批', tone: 'warning' },
  running: { label: '执行中', tone: 'processing' },
  succeeded: { label: '成功', tone: 'success' },
  failed: { label: '失败', tone: 'error' },
  rejected: { label: '已拒绝', tone: 'warning' },
  cancelled: { label: '已取消', tone: 'default' },
}

// asRecord 把未知 JSON 收窄为对象；数组与标量按非对象处理。
function asRecord(value: unknown): Record<string, unknown> | null {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return null
  }
  return value as Record<string, unknown>
}

function stringField(record: Record<string, unknown> | null, key: string): string {
  const value = record?.[key]
  return typeof value === 'string' ? value : ''
}

function countLines(text: string): number {
  return text === '' ? 0 : text.split('\n').length
}

function formatSize(text: string): string {
  const bytes = new TextEncoder().encode(text).length
  if (bytes < 1024) {
    return `${bytes} B`
  }
  return `${(bytes / 1024).toFixed(1)} KB`
}

// prettyJSON 只在用户主动展开时使用，保留完整参数与结果。
function prettyJSON(value: unknown): string {
  if (value === null || value === undefined) {
    return ''
  }
  if (typeof value === 'string') {
    return value
  }
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

function arrayLength(record: Record<string, unknown> | null, key: string): number {
  const value = record?.[key]
  return Array.isArray(value) ? value.length : 0
}

interface ToolFailure {
  message: string
  code?: string
}

// toolFailure 优先使用工具调用的 error_message，其次读取结果里的 error 对象。
// 文件工具的失败结果形如 {"status":"failed","error":{"code":"...","message":"..."}}。
function toolFailure(tool: ToolCallView): ToolFailure | null {
  if (tool.error_message) {
    return { message: tool.error_message }
  }
  const result = asRecord(tool.result_json)
  const error = asRecord(result?.error)
  if (!error) {
    return null
  }
  const message = stringField(error, 'message') || stringField(error, 'code')
  if (!message) {
    return null
  }
  return { message, code: stringField(error, 'code') || undefined }
}

interface ShellView {
  command: string
  output: string
  exitCode?: number
  timedOut: boolean
  truncated: boolean
}

// shellView 提取 shell 工具的命令与结果；其他工具返回 null。
function shellView(tool: ToolCallView): ShellView | null {
  if (tool.name !== 'shell') {
    return null
  }
  const args = asRecord(tool.arguments_json)
  const result = asRecord(tool.result_json)
  return {
    command: stringField(args, 'command'),
    output: stringField(result, 'output'),
    exitCode: typeof result?.exit_code === 'number' ? result.exit_code : undefined,
    timedOut: result?.timed_out === true,
    truncated: result?.truncated === true,
  }
}

interface ToolDescription {
  // action 是面向用户的动作描述，例如“写入文件”“执行命令”。
  action: string
  // target 是动作对象，通常是路径或命令，等宽展示并允许省略。
  target?: string
  // meta 是行数、大小等辅助信息。
  meta?: string
  // body 是展开后的可读详情；原始 JSON 由外层统一折叠展示。
  body?: ReactNode
}

function field(label: string, content: ReactNode): ReactNode {
  return (
    <div className="tool-field">
      <span className="tool-field-label">{label}</span>
      <div className="tool-field-value">{content}</div>
    </div>
  )
}

function codeBlock(content: string): ReactNode {
  return <pre className="tool-code">{content}</pre>
}

// describeTool 把工具名与参数翻译成自然语言动作，并准备可展开的详情。
// 未知工具回退为“调用工具”，避免新工具出现时时间线不可用。
function describeTool(tool: ToolCallView): ToolDescription {
  const args = asRecord(tool.arguments_json)
  const result = asRecord(tool.result_json)
  const path = stringField(args, 'path')
  const content = stringField(args, 'content')
  const oldString = stringField(args, 'old_string')
  const newString = stringField(args, 'new_string')
  const command = stringField(args, 'command')
  const query = stringField(args, 'query')

  switch (tool.name) {
    case 'file_write':
      return {
        action: '写入文件',
        target: path || '未知路径',
        meta: content ? `${countLines(content)} 行 · ${formatSize(content)}` : '空文件',
        body: field('内容', codeBlock(content || '（空文件）')),
      }
    case 'file_patch': {
      const diff = (
        <pre className="tool-code tool-diff">
          {oldString.split('\n').map((line, index) => (
            <div key={`old-${index}`} className="diff-line diff-del">- {line}</div>
          ))}
          {newString.split('\n').map((line, index) => (
            <div key={`new-${index}`} className="diff-line diff-add">+ {line}</div>
          ))}
        </pre>
      )
      return {
        action: '修改文件',
        target: path || '未知路径',
        meta: `${countLines(oldString)} 行 → ${countLines(newString)} 行`,
        body: field('变更', diff),
      }
    }
    case 'file_read': {
      const readContent = stringField(result, 'content')
      return {
        action: '读取文件',
        target: path || '未知路径',
        meta: readContent ? `${countLines(readContent)} 行` : undefined,
        body: field('内容', codeBlock(readContent || prettyJSON(result) || '（无内容）')),
      }
    }
    case 'file_list':
      return {
        action: '浏览目录',
        target: path || '工作区根目录',
        meta: result ? `${arrayLength(result, 'entries')} 项` : undefined,
        body: field('条目', codeBlock(prettyJSON(result) || '（无内容）')),
      }
    case 'file_search':
      return {
        action: '搜索文件',
        target: query ? `“${query}”` : '工作区',
        meta: result ? `${arrayLength(result, 'matches')} 个结果` : undefined,
        body: field('结果', codeBlock(prettyJSON(result) || '（无结果）')),
      }
    case 'file_delete':
      return {
        action: '删除文件',
        target: path || '未知路径',
        body: field('结果', codeBlock(prettyJSON(result) || '（无结果）')),
      }
    case 'shell':
      return {
        action: '执行命令',
        target: command || 'shell',
      }
    case 'submit':
      return {
        action: '提交任务结果',
        body: field('结果', codeBlock(prettyJSON(result) || '（无结果）')),
      }
    default:
      return {
        action: '调用工具',
        target: tool.name,
        body: (
          <>
            {field('参数', codeBlock(prettyJSON(tool.arguments_json) || '（无）'))}
            {field('结果', codeBlock(prettyJSON(tool.result_json) || '（无）'))}
          </>
        ),
      }
  }
}

interface ToolRowProps {
  tool: ToolCallView
}

// ToolRow 默认只展示自然语言动作与状态；点击行展开可读详情，
// 详情底部再折叠完整原始参数，避免默认暴露 JSON。
function ToolRow({ tool }: ToolRowProps) {
  const [open, setOpen] = useState(false)
  // 后端新增状态时兜底展示原始值，避免整条时间线崩溃。
  const status = statusLabels[tool.status] ?? { label: tool.status, tone: 'default' }
  const description = describeTool(tool)
  const shell = shellView(tool)
  const failure = toolFailure(tool)
  const raw = (
    <details className="tool-raw">
      <summary>查看原始参数与结果</summary>
      {field('参数', codeBlock(prettyJSON(tool.arguments_json) || '（无）'))}
      {field('结果', codeBlock(prettyJSON(tool.result_json) || '（无）'))}
    </details>
  )

  return (
    <div className={`tool-row tool-${tool.status}`} data-tool={tool.name}>
      <button
        type="button"
        className="tool-summary"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <span className="tool-state-dot" />
        <span className="tool-action">{description.action}</span>
        {description.target && (
          <span className="tool-target" title={description.target}>
            {description.target}
          </span>
        )}
        {description.meta && <span className="tool-meta">{description.meta}</span>}
        <span className={`tool-status tool-status-${status.tone}`}>{status.label}</span>
        <span className={`tool-chevron${open ? ' open' : ''}`} aria-hidden="true">
          ⌄
        </span>
      </button>
      {failure && (
        <div className="tool-error-line">
          {failure.message}
          {failure.code && failure.code !== failure.message && (
            <span className="tool-error-code">{failure.code}</span>
          )}
        </div>
      )}
      {shell && (
        <TerminalBlock
          command={shell.command}
          output={shell.output}
          exitCode={shell.exitCode}
          timedOut={shell.timedOut}
          truncated={shell.truncated}
          title="终端输出"
        />
      )}
      {open && (
        <div className="tool-details">
          {description.body}
          {raw}
        </div>
      )}
    </div>
  )
}

// ToolTimeline 展示审批与工具调用；整体可收起，单条工具可展开详情。
export default function ToolTimeline({ snapshot, onDecide }: ToolTimelineProps) {
  const collapsed = useRunStore((state) => state.timelineCollapsed)
  const setCollapsed = useRunStore((state) => state.setTimelineCollapsed)
  if (!snapshot || (snapshot.tool_calls.length === 0 && snapshot.approvals.length === 0)) {
    return null
  }
  const pendingApprovals = snapshot.approvals.filter((approval) => approval.status === 'pending')
  return (
    <div className="tool-timeline">
      <Button
        size="small"
        className="timeline-toggle"
        onClick={() => setCollapsed(!collapsed)}
      >
        {collapsed ? '展开工具时间线' : '收起工具时间线'}
      </Button>
      {!collapsed && (
        <>
          {pendingApprovals.map((approval) => (
            <ApprovalCard
              key={approval.id}
              approval={approval}
              name={snapshot.tool_calls.find((tool) => tool.id === approval.tool_call_id)?.name}
              onDecide={onDecide}
            />
          ))}
          {snapshot.tool_calls.map((tool) => (
            <ToolRow key={tool.id} tool={tool} />
          ))}
        </>
      )}
    </div>
  )
}
