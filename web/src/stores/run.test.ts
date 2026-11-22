import { describe, expect, it } from 'vitest'
import type { RunEvent } from '../api/types'
import { createRunStore } from './run'

// event 构造 SSE Envelope，缺省字段补齐为合法值。
function event(
  partial: Partial<RunEvent> & Pick<RunEvent, 'run_id' | 'seq' | 'type'>,
): RunEvent {
  return {
    id: `evt-${partial.run_id}-${partial.seq}`,
    created_at: '2026-11-18T10:00:00Z',
    payload: {},
    ...partial,
  }
}

describe('createRunStore', () => {
  it('按 run_id + seq 去重并只追加一次文本', () => {
    const store = createRunStore()
    const envelope = event({ run_id: 'r1', seq: 7, type: 'message.delta', payload: { message_id: 'm1', delta: '你好' } })
    store.getState().applyEvent(envelope)
    store.getState().applyEvent(envelope)
    expect(store.getState().deltas.m1).toBe('你好')
    expect(store.getState().timelineEvents.r1).toHaveLength(1)
  })

  it('不同 Run 的相同 seq 互不影响，增量按到达顺序拼接', () => {
    const store = createRunStore()
    store.getState().applyEvent(event({ run_id: 'r1', seq: 1, type: 'message.delta', payload: { message_id: 'm1', delta: '你' } }))
    store.getState().applyEvent(event({ run_id: 'r2', seq: 1, type: 'message.delta', payload: { message_id: 'm2', delta: '好' } }))
    store.getState().applyEvent(event({ run_id: 'r1', seq: 2, type: 'message.delta', payload: { message_id: 'm1', delta: '好' } }))
    expect(store.getState().deltas.m1).toBe('你好')
    expect(store.getState().deltas.m2).toBe('好')
  })

  it('思考事件按消息 ID 累积，与正文增量互不影响', () => {
    const store = createRunStore()
    store.getState().applyEvent(
      event({ run_id: 'r1', seq: 1, type: 'message.reasoning', payload: { message_id: 'm1', delta: '先分析' } }),
    )
    store.getState().applyEvent(
      event({ run_id: 'r1', seq: 2, type: 'message.reasoning', payload: { message_id: 'm1', delta: '需求' } }),
    )
    store.getState().applyEvent(
      event({ run_id: 'r1', seq: 3, type: 'message.delta', payload: { message_id: 'm1', delta: '答案' } }),
    )
    expect(store.getState().reasoning.m1).toBe('先分析需求')
    expect(store.getState().deltas.m1).toBe('答案')
  })

  it('resetRun 清空增量与连接状态，但保留草稿', () => {
    const store = createRunStore()
    store.getState().setDraft('s1', '保留草稿')
    store.getState().setConnection('connected')
    store.getState().applyEvent(event({ run_id: 'r1', seq: 3, type: 'message.delta', payload: { message_id: 'm1', delta: '文本' } }))
    store.getState().resetRun('r1')
    expect(store.getState().deltas).toEqual({})
    expect(store.getState().connection).toBeNull()
    expect(store.getState().drafts.s1).toBe('保留草稿')
    // 重置后同一事件可再次应用（重连补洞场景）。
    store.getState().applyEvent(event({ run_id: 'r1', seq: 3, type: 'message.delta', payload: { message_id: 'm1', delta: '文本' } }))
    expect(store.getState().deltas.m1).toBe('文本')
  })
})