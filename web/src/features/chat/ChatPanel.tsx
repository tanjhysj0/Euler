import { Alert, Button, Empty, Input, message, Modal, Space, Tag, Typography } from 'antd'
import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { APIError } from '../../api/client'
import {
  useApprovalDecision,
  useCancelRun,
  useMessages,
  useRunSnapshot,
  useStartRun,
} from '../../api/queries'
import type { ConnectionStatus } from '../../stores/run'
import type { RunStatus, SessionView } from '../../api/types'
import { useRunStore } from '../../stores/run'
import MessageList from './MessageList'
import ToolTimeline from './ToolTimeline'
import { isTerminalStatus, useRunEvents } from './useRunEvents'

interface ChatPanelProps {
  session: SessionView
}

const runStatusLabels: Record<RunStatus, { label: string; color: string }> = {
  queued: { label: '排队中', color: 'default' },
  running: { label: '运行中', color: 'processing' },
  waiting_approval: { label: '等待审批', color: 'warning' },
  succeeded: { label: '已完成', color: 'success' },
  failed: { label: '失败', color: 'error' },
  cancelled: { label: '已取消', color: 'default' },
  interrupted: { label: '已中断', color: 'default' },
}

const connectionLabels: Record<ConnectionStatus, string> = {
  connecting: '连接中',
  connected: '已连接',
  reconnecting: '重连中',
}

// ChatPanel 组合消息列表、工具时间线与输入区；权威数据来自 REST 快照，
// SSE 增量只用于 streaming 展示。刷新恢复以最后一条带 run_id 的消息为准，
// 终态 Run 也要恢复快照以展示工具时间线与错误信息。
export default function ChatPanel({ session }: ChatPanelProps) {
  const queryClient = useQueryClient()
  const messagesQuery = useMessages(session.id)
  const messages = messagesQuery.data ?? []
  // 刷新恢复：只按最后一条带 run_id 的消息找回 Run。取最后一条本身即可
  // 避免更早的 cancelled/interrupted 消息覆盖后续 Run 的状态。
  const lastRunMessage = [...messages].reverse().find((message) => message.run_id)
  const restoreRunId = lastRunMessage?.run_id ?? undefined
  const [runId, setRunId] = useState<string | undefined>(restoreRunId)
  // 渲染期比较上一次值来同步 state（React 推荐模式），避免 effect 级联渲染。
  const [prevRestoreRunId, setPrevRestoreRunId] = useState<string | undefined>(restoreRunId)
  if (restoreRunId !== prevRestoreRunId) {
    setPrevRestoreRunId(restoreRunId)
    if (restoreRunId) {
      setRunId(restoreRunId)
    }
  }
  const snapshot = useRunSnapshot(runId).data
  useRunEvents(snapshot)
  const active = snapshot !== undefined && !isTerminalStatus(snapshot.run.status)

  const draft = useRunStore((state) => state.drafts[session.id] ?? '')
  const connection = useRunStore((state) => state.connection)
  const deltas = useRunStore((state) => state.deltas)
  const reasoning = useRunStore((state) => state.reasoning)
  const setDraft = useRunStore((state) => state.setDraft)

  const startRun = useStartRun(session.id)
  const cancelRun = useCancelRun(runId)
  const decide = useApprovalDecision(runId)
  const [sendError, setSendError] = useState<string | null>(null)
  const [stopOpen, setStopOpen] = useState(false)
  const [showWorkspaceWarning, setShowWorkspaceWarning] = useState(false)

  async function send() {
    const content = draft.trim()
    if (active || startRun.isPending || content === '') {
      return
    }
    setSendError(null)
    try {
      const result = await startRun.mutateAsync(content)
      setDraft(session.id, '')
      setShowWorkspaceWarning(result.workspace_warning)
      useRunStore.getState().setActive(session.id, result.run.id)
      setRunId(result.run.id)
    } catch (err) {
      if (err instanceof APIError && err.code === 'active_run_exists') {
        // 本地状态落后于服务端：重取消息重新发现活跃 Run。
        void queryClient.invalidateQueries({ queryKey: ['messages', session.id] })
      }
      setSendError(err instanceof APIError ? err.message : '发送失败')
    }
  }

  async function onDecide(approvalId: string, approved: boolean, reason: string) {
    await decide.mutateAsync({ approvalId, approved, reason })
  }

  // 停止失败时保留弹窗并提示错误，允许用户重试；成功才关闭。
  async function stopRun() {
    try {
      await cancelRun.mutateAsync()
      setStopOpen(false)
    } catch (err) {
      message.error(err instanceof APIError ? err.message : '停止失败')
    }
  }

  const runStatus = snapshot ? runStatusLabels[snapshot.run.status] : null

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Space wrap>
        {runStatus && <Tag color={runStatus.color}>{runStatus.label}</Tag>}
        {active && connection && (
          <Typography.Text type="secondary">{connectionLabels[connection]}</Typography.Text>
        )}
        {active && (
          <Button danger autoInsertSpace={false} onClick={() => setStopOpen(true)}>
            停止
          </Button>
        )}
      </Space>
      {snapshot?.run.status === 'failed' && (
        <Alert
          type="error"
          showIcon
          title={snapshot.run.error_message ?? snapshot.run.error_code ?? '任务失败'}
        />
      )}
      {sendError && <Alert type="error" showIcon title={sendError} />}
      {showWorkspaceWarning && (
        <Alert
          type="warning"
          showIcon
          title="其他会话正在此目录执行任务，注意并发修改风险"
        />
      )}
      {messagesQuery.isPending ? (
        <Empty description="加载消息中…" />
      ) : (
        <MessageList messages={messages} deltas={deltas} reasoning={reasoning} />
      )}
      <ToolTimeline snapshot={snapshot} onDecide={onDecide} />
      <Input.TextArea
        placeholder="输入消息"
        value={draft}
        autoSize={{ minRows: 2, maxRows: 6 }}
        onChange={(event) => setDraft(session.id, event.target.value)}
        onKeyDown={(event) => {
          if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
            event.preventDefault()
            void send()
          }
        }}
      />
      <Button
        type="primary"
        autoInsertSpace={false}
        disabled={active || startRun.isPending || draft.trim() === ''}
        onClick={() => void send()}
      >
        发送
      </Button>
      <Modal
        open={stopOpen}
        title="停止当前任务"
        okText="确认停止"
        cancelText="取消"
        confirmLoading={cancelRun.isPending}
        onOk={() => {
          void stopRun()
        }}
        onCancel={() => setStopOpen(false)}
      />
    </Space>
  )
}
