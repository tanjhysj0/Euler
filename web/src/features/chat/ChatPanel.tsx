import { Alert, Button, Empty, Input, message, Modal, Select, Typography } from 'antd'
import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { APIError } from '../../api/client'
import {
  useApprovalDecision,
  useCancelRun,
  useMessages,
  useModelProfiles,
  useRunSnapshot,
  useStartRun,
  useUpdateSessionModel,
  useUpdateSessionWorkspace,
} from '../../api/queries'
import type { ConnectionStatus } from '../../stores/run'
import type { RunStatus, SessionView } from '../../api/types'
import { useRunStore } from '../../stores/run'
import MessageList from './MessageList'
import PartsTimeline from './PartsTimeline'
import RunTimeline from './RunTimeline'
import { isTerminalStatus, useRunEvents } from './useRunEvents'
import DirectoryPicker, { FolderIcon } from '../workspace/DirectoryPicker'

interface ChatPanelProps {
  session: SessionView
}

// workspaceLabel 取目录的最后一级作为按钮文案，根目录直接显示 /。
function workspaceLabel(path: string): string {
  const parts = path.split('/').filter((part) => part !== '')
  return parts.length === 0 ? '/' : parts[parts.length - 1]
}

const runStatusLabels: Record<RunStatus, string> = {
  queued: '排队中',
  running: '运行中',
  waiting_approval: '等待审批',
  succeeded: '已完成',
  failed: '失败',
  cancelled: '已取消',
  interrupted: '已中断',
}

const connectionLabels: Record<ConnectionStatus, string> = {
  connecting: '连接中',
  connected: '已连接',
  reconnecting: '重连中',
}

// ChatPanel 组合消息列表、对话时间线与输入区；权威数据来自 REST 快照，
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
  const profilesQuery = useModelProfiles()
  const updateModel = useUpdateSessionModel(session.id)
  const updateWorkspace = useUpdateSessionWorkspace(session.id)
  const [sendError, setSendError] = useState<string | null>(null)
  const [stopOpen, setStopOpen] = useState(false)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [showWorkspaceWarning, setShowWorkspaceWarning] = useState(false)

  // 模型列表可能尚未加载或缺少当前会话的配置，补一个当前项保证下拉框
  // 始终显示模型名称而不是原始 ID。
  const profiles = profilesQuery.data ?? []
  const modelOptions = profiles.map((profile) => ({
    value: profile.id,
    label: profile.name,
  }))
  if (!profiles.some((profile) => profile.id === session.model.id)) {
    modelOptions.unshift({ value: session.model.id, label: session.model.name })
  }

  async function changeModel(profileId: string) {
    if (profileId === session.model.id) {
      return
    }
    try {
      await updateModel.mutateAsync(profileId)
    } catch (err) {
      message.error(err instanceof APIError ? err.message : '切换模型失败')
    }
  }

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

  // 选择工作目录：目录未变化时直接关闭，否则更新会话并刷新文件树。
  async function chooseWorkspace(path: string) {
    if (path === session.workspace.path) {
      setPickerOpen(false)
      return
    }
    try {
      await updateWorkspace.mutateAsync(path)
      setPickerOpen(false)
    } catch (err) {
      message.error(err instanceof APIError ? err.message : '切换工作目录失败')
    }
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
    <div className="chat-panel">
      <div className="run-line">
        {runStatus && snapshot && (
          <span className={`run-status run-status-${snapshot.run.status}`}>
            <i className="run-status-dot" />
            {runStatus}
          </span>
        )}
        {active && connection && (
          <Typography.Text type="secondary" className="connection-text">
            {connectionLabels[connection]}
          </Typography.Text>
        )}
      </div>
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
      <div className="chat-scroll">
        {messagesQuery.isPending ? (
          <Empty description="加载消息中…" />
        ) : (
          <MessageList
            messages={messages}
            deltas={deltas}
            reasoning={reasoning}
            renderAssistant={(message) => {
              if (!message.run_id) {
                return undefined
              }
              // 进行中的 Run 用 SSE 实时还原；终态消息以落库 parts 为唯一来源。
              if (message.status !== 'streaming') {
                return <PartsTimeline message={message} />
              }
              return (
                <RunTimeline
                  runId={message.run_id}
                  messageId={message.id}
                  fallbackContent={message.content}
                  streaming
                  snapshot={message.run_id === runId ? snapshot : undefined}
                  onDecide={onDecide}
                />
              )
            }}
          />
        )}
      </div>
      <div className="composer">
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
        <div className="composer-actions">
          <div className="composer-left">
            <Button
              className="composer-workspace"
              size="small"
              type="text"
              icon={<FolderIcon />}
              title={session.workspace.path}
              aria-label="选择工作目录"
              disabled={active || updateWorkspace.isPending}
              onClick={() => setPickerOpen(true)}
            >
              {workspaceLabel(session.workspace.path)}
            </Button>
            <Select
              className="composer-model"
              size="small"
              variant="borderless"
              aria-label="当前模型"
              value={session.model.id}
              options={modelOptions}
              loading={profilesQuery.isPending}
              disabled={active || updateModel.isPending}
              popupMatchSelectWidth={false}
              onChange={(profileId: string) => void changeModel(profileId)}
            />
          </div>
          {active ? (
            <Button danger autoInsertSpace={false} onClick={() => setStopOpen(true)}>
              停止
            </Button>
          ) : (
            <Button
              type="primary"
              autoInsertSpace={false}
              disabled={startRun.isPending || draft.trim() === ''}
              onClick={() => void send()}
            >
              发送
            </Button>
          )}
        </div>
      </div>
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
      <DirectoryPicker
        open={pickerOpen}
        initialPath=""
        confirmLoading={updateWorkspace.isPending}
        onCancel={() => setPickerOpen(false)}
        onSelect={chooseWorkspace}
      />
    </div>
  )
}
