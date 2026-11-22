import { describe, expect, it } from 'vitest'
import type { RunEvent } from '../../api/types'
import { buildRunTimeline, toTimelineEvent } from './timeline'
import type { TimelineEvent } from './timeline'

function event(seq: number, type: string, payload: Record<string, unknown>): RunEvent {
  return {
    id: `evt-${seq}`,
    run_id: 'r1',
    seq,
    type,
    payload,
    created_at: '2026-11-22T10:00:00Z',
  }
}

describe('runTimeline', () => {
  it('把事件裁剪为最小时间线事件，忽略无关事件', () => {
    expect(toTimelineEvent(event(1, 'message.delta', { message_id: 'm1', delta: '你好' }))).toEqual({
      seq: 1,
      type: 'message.delta',
      messageId: 'm1',
      delta: '你好',
    })
    expect(toTimelineEvent(event(2, 'tool.started', { tool_call_id: 'tc1' }))).toEqual({
      seq: 2,
      type: 'tool.started',
      toolCallId: 'tc1',
    })
    expect(toTimelineEvent(event(3, 'message.started', { id: 'm1' }))).toBeNull()
  })

  it('按文本、工具、文本的顺序重建交错时间线', () => {
    const events: TimelineEvent[] = [
      { seq: 1, type: 'message.reasoning', messageId: 'm1', delta: '先想一下' },
      { seq: 2, type: 'message.delta', messageId: 'm1', delta: '我先检查目录。' },
      { seq: 3, type: 'tool.started', toolCallId: 'tc1' },
      { seq: 4, type: 'tool.completed', toolCallId: 'tc1' },
      { seq: 5, type: 'message.delta', messageId: 'm1', delta: '最终回答。' },
    ]
    const items = buildRunTimeline(events)
    expect(items.map((item) => item.kind)).toEqual(['reasoning', 'text', 'tool', 'text'])
    expect(items[1]).toMatchObject({ kind: 'text', content: '我先检查目录。' })
    expect(items[3]).toMatchObject({ kind: 'text', content: '最终回答。' })
  })

  it('连续的同段增量合并，工具不会重复插入', () => {
    const events: TimelineEvent[] = [
      { seq: 1, type: 'message.delta', messageId: 'm1', delta: '你' },
      { seq: 2, type: 'message.delta', messageId: 'm1', delta: '好' },
      { seq: 3, type: 'tool.started', toolCallId: 'tc1' },
      { seq: 4, type: 'tool.completed', toolCallId: 'tc1' },
    ]
    const items = buildRunTimeline(events)
    expect(items).toHaveLength(2)
    expect(items[0]).toMatchObject({ kind: 'text', content: '你好' })
    expect(items[1]).toMatchObject({ kind: 'tool', toolCallId: 'tc1' })
  })
})
