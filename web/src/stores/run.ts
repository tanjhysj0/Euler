import { create } from 'zustand'
import { createStore } from 'zustand/vanilla'
import type { StateCreator } from 'zustand'
import type { RunEvent } from '../api/types'

export type ConnectionStatus = 'connecting' | 'connected' | 'reconnecting'

// RunStoreState 只保存 SSE 增量、去重键、连接状态、活跃 Run、草稿与面板状态；
// 权威数据（消息、工具调用、审批）始终来自 REST 快照。
export interface RunStoreState {
  deltas: Record<string, string>
  // reasoning 按消息 ID 累积模型思考增量；思考过程不落库，刷新后丢失。
  reasoning: Record<string, string>
  lastSeq: Record<string, number>
  seenEventKeys: Set<string>
  connection: ConnectionStatus | null
  activeBySession: Record<string, string>
  drafts: Record<string, string>
  timelineCollapsed: boolean
  applyEvent: (envelope: RunEvent) => void
  setDraft: (sessionId: string, draft: string) => void
  setActive: (sessionId: string, runId: string | null) => void
  setConnection: (status: ConnectionStatus | null) => void
  setTimelineCollapsed: (collapsed: boolean) => void
  resetRun: (runId: string) => void
}

const initializer: StateCreator<RunStoreState> = (set, get) => ({
  deltas: {},
  reasoning: {},
  lastSeq: {},
  seenEventKeys: new Set<string>(),
  connection: null,
  activeBySession: {},
  drafts: {},
  timelineCollapsed: false,

  applyEvent(envelope) {
    const key = `${envelope.run_id}:${envelope.seq}`
    if (get().seenEventKeys.has(key)) {
      return
    }
    const seenEventKeys = new Set(get().seenEventKeys)
    seenEventKeys.add(key)
    const lastSeq = {
      ...get().lastSeq,
      [envelope.run_id]: Math.max(get().lastSeq[envelope.run_id] ?? 0, envelope.seq),
    }
    if (envelope.type === 'message.delta') {
      const payload = envelope.payload as { message_id: string; delta: string }
      set({
        seenEventKeys,
        lastSeq,
        deltas: {
          ...get().deltas,
          [payload.message_id]: (get().deltas[payload.message_id] ?? '') + payload.delta,
        },
      })
      return
    }
    if (envelope.type === 'message.reasoning') {
      const payload = envelope.payload as { message_id: string; delta: string }
      set({
        seenEventKeys,
        lastSeq,
        reasoning: {
          ...get().reasoning,
          [payload.message_id]: (get().reasoning[payload.message_id] ?? '') + payload.delta,
        },
      })
      return
    }
    set({ seenEventKeys, lastSeq })
  },

  setDraft(sessionId, draft) {
    set({ drafts: { ...get().drafts, [sessionId]: draft } })
  },

  setActive(sessionId, runId) {
    const activeBySession = { ...get().activeBySession }
    if (runId === null) {
      delete activeBySession[sessionId]
    } else {
      activeBySession[sessionId] = runId
    }
    set({ activeBySession })
  },

  setConnection(status) {
    set({ connection: status })
  },

  setTimelineCollapsed(collapsed) {
    set({ timelineCollapsed: collapsed })
  },

  // 终态后清空本地增量，让权威快照接管；同一 Run 的事件可再次应用（补洞）。
  resetRun(runId) {
    const lastSeq = { ...get().lastSeq }
    delete lastSeq[runId]
    const seenEventKeys = new Set(
      [...get().seenEventKeys].filter((key) => !key.startsWith(`${runId}:`)),
    )
    set({ deltas: {}, lastSeq, seenEventKeys, connection: null })
  },
})

// createRunStore 供测试创建隔离实例；useRunStore 是绑定全局实例的 hook，
// 组件外可通过 useRunStore.getState() 读写。
export function createRunStore() {
  return createStore<RunStoreState>(initializer)
}

export const useRunStore = create<RunStoreState>(initializer)