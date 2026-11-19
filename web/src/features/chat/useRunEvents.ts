import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef } from 'react'
import type { RunEvent, RunSnapshot, RunStatus } from '../../api/types'
import { useRunStore } from '../../stores/run'

const terminalStatuses = new Set<RunStatus>(['succeeded', 'failed', 'cancelled', 'interrupted'])
const terminalEvents = new Set(['run.completed', 'run.cancelled', 'run.interrupted', 'run.failed'])

export function isTerminalStatus(status: RunStatus): boolean {
  return terminalStatuses.has(status)
}

// useRunEvents 从快照的 last_event_seq 建立 SSE 连接；EventSource 自动重连时
// 浏览器携带 Last-Event-ID，服务端同时按 after 参数回放。事件先按 run_id+seq
// 去重，再按类型更新增量或失效权威查询；终态关闭连接并清空本地增量。
export function useRunEvents(snapshot: RunSnapshot | undefined) {
  const queryClient = useQueryClient()
  const run = snapshot?.run
  const runId = run?.id
  const sessionId = run?.session_id
  const active = run !== undefined && !terminalStatuses.has(run.status)

  // 起始 seq 随快照同步到 ref；主 effect 只依赖 runId/active，快照刷新
  // 不会触发重连，重连时仍从最新 seq 之后回放。
  const seqRef = useRef(0)
  useEffect(() => {
    seqRef.current = snapshot?.last_event_seq ?? 0
  }, [snapshot])

  useEffect(() => {
    if (!active || runId === undefined || sessionId === undefined) {
      return
    }
    useRunStore.getState().setActive(sessionId, runId)
    useRunStore.getState().setConnection('connecting')
    const source = new EventSource(`/api/v1/runs/${runId}/events?after=${seqRef.current}`)
    let closed = false

    source.onopen = () => {
      if (!closed) {
        useRunStore.getState().setConnection('connected')
      }
    }
    // onerror 只表示连接中断，EventSource 会自动重连，不能把 Run 误判为失败。
    source.onerror = () => {
      if (closed) {
        return
      }
      if (source.readyState === EventSource.CLOSED) {
        // 浏览器已放弃重连（如 HTTP 4xx）：关闭连接并失效权威查询兜底，
        // 不做自动轮询；若 Run 仍活跃，由用户刷新页面重建连接。
        closed = true
        source.close()
        useRunStore.getState().setConnection(null)
        void queryClient.invalidateQueries({ queryKey: ['runs', runId] })
        void queryClient.invalidateQueries({ queryKey: ['messages', sessionId] })
        return
      }
      useRunStore.getState().setConnection('reconnecting')
    }
    source.onmessage = (event) => {
      if (closed) {
        return
      }
      let envelope: RunEvent
      try {
        envelope = JSON.parse(event.data) as RunEvent
      } catch {
        return
      }
      useRunStore.getState().applyEvent(envelope)
      if (envelope.type.startsWith('tool.') || envelope.type.startsWith('approval.')) {
        void queryClient.invalidateQueries({ queryKey: ['runs', runId] })
      }
      // 工具完成可能修改了工作区产物，刷新文件树与预览。
      if (envelope.type === 'tool.completed') {
        void queryClient.invalidateQueries({ queryKey: ['files'] })
      }
      if (envelope.type === 'message.started') {
        void queryClient.invalidateQueries({ queryKey: ['messages', sessionId] })
      }
      if (terminalEvents.has(envelope.type)) {
        closed = true
        source.close()
        useRunStore.getState().setConnection(null)
        useRunStore.getState().setActive(sessionId, null)
        void (async () => {
          await queryClient.invalidateQueries({ queryKey: ['messages', sessionId] })
          await queryClient.invalidateQueries({ queryKey: ['runs', runId] })
          void queryClient.invalidateQueries({ queryKey: ['sessions'] })
          void queryClient.invalidateQueries({ queryKey: ['files'] })
          useRunStore.getState().resetRun(runId)
        })()
      }
    }

    // StrictMode 双挂载、切换会话与组件卸载都必须关闭旧连接。
    return () => {
      closed = true
      source.close()
      useRunStore.getState().setConnection(null)
    }
  }, [runId, sessionId, active, queryClient])
}
