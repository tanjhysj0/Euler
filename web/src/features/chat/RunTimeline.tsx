import { Button } from 'antd'
import { Fragment, useMemo, useState } from 'react'
import type { RunSnapshot } from '../../api/types'
import { useRunSnapshot } from '../../api/queries'
import { useRunStore } from '../../stores/run'
import MarkdownView from '../../components/MarkdownView'
import ApprovalCard from './ApprovalCard'
import { ToolRow } from './ToolRow'
import { buildRunTimeline, mergeStreamingText } from './timeline'
import type { TimelineItem } from './timeline'

interface RunTimelineProps {
  runId: string
  // messageId 用于在事件缺失时回退到该条助手消息的流式增量。
  messageId: string
  fallbackContent: string
  streaming: boolean
  // 当前 Run 由 ChatPanel 传入实时快照；历史 Run 自行加载。
  snapshot?: RunSnapshot
  onDecide: (approvalId: string, approved: boolean, reason: string) => Promise<void>
}

// RunTimeline 在 Run 进行中按 SSE 事件顺序还原真实对话过程：
// 思考、文本与工具调用可以交错出现，最终回答位于工具执行之后。
// 连接时服务端会回放该 Run 的全部持久化事件，因此刷新后也能完整重建。
export default function RunTimeline({
  runId,
  messageId,
  fallbackContent,
  streaming,
  snapshot,
  onDecide,
}: RunTimelineProps) {
  const liveEvents = useRunStore((state) => state.timelineEvents[runId])
  const delta = useRunStore((state) => state.deltas[messageId] ?? '')
  const reasoningDelta = useRunStore((state) => state.reasoning[messageId] ?? '')
  // 服务端 content 可能落后于实时增量，按重叠部分去重后拼接；
  // 终态后 content 是权威全文，不再拼接本地增量。
  const fallbackText = streaming ? mergeStreamingText(fallbackContent, delta) : fallbackContent
  const fetchedSnapshot = useRunSnapshot(snapshot ? undefined : runId).data
  const effectiveSnapshot = snapshot ?? fetchedSnapshot
  const [collapsed, setCollapsed] = useState(false)

  const items = useMemo(() => {
    const built = buildRunTimeline(liveEvents ?? [])
    const presentTools = new Set(
      built.filter((item) => item.kind === 'tool').map((item) => item.toolCallId),
    )
    const missingTools: TimelineItem[] = (effectiveSnapshot?.tool_calls ?? [])
      .filter((tool) => !presentTools.has(tool.id))
      .map((tool) => ({
        kind: 'tool' as const,
        key: `tool-${tool.id}`,
        toolCallId: tool.id,
      }))
    const result = [...built, ...missingTools]
    const reasoningItem: TimelineItem | null =
      !built.some((item) => item.kind === 'reasoning') && reasoningDelta !== ''
        ? {
            kind: 'reasoning',
            key: `reasoning-${messageId}`,
            messageId,
            content: reasoningDelta,
          }
        : null
    // 文本以事件分段为主；content 用于补齐事件历史缺失的部分。
    const textItems = built.filter(
      (item): item is Extract<TimelineItem, { kind: 'text' }> => item.kind === 'text',
    )
    const eventText = textItems.map((item) => item.content).join('')
    if (textItems.length === 0) {
      if (fallbackText !== '') {
        result.push({
          kind: 'text',
          key: `fallback-${messageId}`,
          messageId,
          content: fallbackText,
        })
      }
    } else if (streaming && fallbackText !== eventText && fallbackText.endsWith(eventText)) {
      // 挂载前已刷盘的文本不在实时事件里，补到首个文本段前。
      const prefix = fallbackText.slice(0, fallbackText.length - eventText.length)
      textItems[0].content = prefix + textItems[0].content
    } else if (!streaming && fallbackText !== eventText && fallbackText !== '') {
      // 事件历史不完整时，以权威 content 覆盖分段，避免缺字或重复。
      const replaced: TimelineItem[] = [
        ...built.filter((item) => item.kind !== 'text'),
        ...missingTools,
        {
          kind: 'text',
          key: `fallback-${messageId}`,
          messageId,
          content: fallbackText,
        },
      ]
      if (reasoningItem) {
        replaced.unshift(reasoningItem)
      }
      return replaced
    }
    if (reasoningItem) {
      result.unshift(reasoningItem)
    }
    return result
  }, [liveEvents, effectiveSnapshot, fallbackText, messageId, reasoningDelta, streaming])

  if (items.length === 0) {
    return null
  }

  const toolMap = new Map((effectiveSnapshot?.tool_calls ?? []).map((tool) => [tool.id, tool]))
  const pendingApprovals = new Map(
    (effectiveSnapshot?.approvals ?? [])
      .filter((approval) => approval.status === 'pending')
      .map((approval) => [approval.tool_call_id, approval]),
  )
  const toolCount = items.filter((item) => item.kind === 'tool').length
  const renderedApprovals = new Set<string>()

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
        {items.map((item) => {
          if (item.kind === 'text') {
            return <MarkdownView key={item.key} content={item.content} />
          }
          if (item.kind === 'reasoning') {
            return (
              <details className="reasoning-block" key={item.key}>
                <summary>{streaming ? '思考中…' : '思考过程'}</summary>
                <div className="reasoning-text">{item.content}</div>
              </details>
            )
          }
          if (collapsed) {
            return null
          }
          const tool = toolMap.get(item.toolCallId)
          if (!tool) {
            return null
          }
          const approval = pendingApprovals.get(tool.id)
          if (!approval) {
            return <ToolRow key={item.key} tool={tool} />
          }
          renderedApprovals.add(approval.id)
          return (
            <Fragment key={item.key}>
              <ToolRow tool={tool} />
              <ApprovalCard approval={approval} name={tool.name} onDecide={onDecide} />
            </Fragment>
          )
        })}
        {/* 审批卡片不随工具列表收起，避免审批入口被隐藏。 */}
        {[...pendingApprovals.values()]
          .filter((approval) => !renderedApprovals.has(approval.id))
          .map((approval) => (
            <ApprovalCard
              key={approval.id}
              approval={approval}
              name={toolMap.get(approval.tool_call_id)?.name}
              onDecide={onDecide}
            />
          ))}
      </div>
    </div>
  )
}
