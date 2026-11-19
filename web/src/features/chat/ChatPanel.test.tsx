import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type {
  ApprovalView,
  MessageView,
  RunEvent,
  RunSnapshot,
  RunStatus,
  SessionView,
  StartRunResult,
  ToolCallStatus,
  ToolCallView,
} from '../../api/types'
import { useRunStore } from '../../stores/run'
import { renderWithProviders } from '../../test/utils'
import ChatPanel from './ChatPanel'

// FakeEventSource 记录实例与 URL，测试手动触发 open/message/error。
class FakeEventSource {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSED = 2
  static instances: FakeEventSource[] = []
  url: string
  closed = false
  readyState = FakeEventSource.CONNECTING
  onopen: ((event: unknown) => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onerror: ((event: unknown) => void) | null = null

  constructor(url: string) {
    this.url = url
    FakeEventSource.instances.push(this)
  }

  close() {
    this.closed = true
    this.readyState = FakeEventSource.CLOSED
  }

  emitOpen() {
    this.readyState = FakeEventSource.OPEN
    this.onopen?.({})
  }

  emitMessage(envelope: RunEvent) {
    this.onmessage?.({ data: JSON.stringify(envelope) })
  }

  emitError() {
    this.onerror?.({})
  }
}

const session: SessionView = {
  id: 's1',
  title: '会话一',
  status: 'active',
  workspace: { id: 'w1', path: '/tmp/ws' },
  model: { id: 'model-1', name: '测试模型', base_url: 'https://example.com/v1', model: 'gpt-test' },
  created_at: '2026-11-18T10:00:00Z',
  updated_at: '2026-11-18T10:00:00Z',
  last_active_at: '2026-11-18T10:00:00Z',
}
const session2: SessionView = { ...session, id: 's2', title: '会话二' }

const userMessage: MessageView = {
  id: 'm1',
  run_id: 'r1',
  session_seq: 1,
  role: 'user',
  content: '请打个招呼',
  status: 'completed',
  created_at: '2026-11-18T10:00:00Z',
}
const streamingMessage: MessageView = {
  id: 'm2',
  run_id: 'r1',
  session_seq: 2,
  role: 'assistant',
  content: '',
  status: 'streaming',
  created_at: '2026-11-18T10:00:01Z',
}

function makeSnapshot(status: RunStatus): RunSnapshot {
  return {
    run: {
      id: 'r1',
      session_id: 's1',
      status,
      next_event_seq: 6,
      error_code: null,
      error_message: null,
      created_at: '2026-11-18T10:00:00Z',
      started_at: '2026-11-18T10:00:00Z',
      finished_at: null,
    },
    tool_calls: [],
    approvals: [],
    last_event_seq: 5,
  }
}

const startResult: StartRunResult = {
  ...makeSnapshot('running'),
  user_message: { ...userMessage, id: 'm3', session_seq: 3, content: '新的任务' },
  assistant_message: { ...streamingMessage, id: 'm4', session_seq: 4 },
  workspace_warning: false,
}

function envelope(partial: Partial<RunEvent> & Pick<RunEvent, 'seq' | 'type'>): RunEvent {
  return {
    id: `evt-r1-${partial.seq}`,
    run_id: 'r1',
    created_at: '2026-11-18T10:00:02Z',
    payload: {},
    ...partial,
  }
}

// 每个用例重置的可变后端状态。
let messages: MessageView[] = []
let snapshot: RunSnapshot = makeSnapshot('running')
let runFetchCount = 0
let decisionStatus = 200
let cancelStatus = 200
let workspaceWarning = false
let calls: { url: string; method: string; body?: unknown }[] = []

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body }) as Response
const fail = (status: number, body: unknown) =>
  ({ ok: false, status, json: async () => body }) as Response

const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = String(input)
  const method = init?.method ?? 'GET'
  const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
  calls.push({ url, method, body })
  if (url.startsWith('/api/v1/sessions/s1/messages')) {
    return ok({ messages, next_after: 0 })
  }
  if (url.startsWith('/api/v1/sessions/s2/messages')) {
    return ok({ messages: [], next_after: 0 })
  }
  if (url === '/api/v1/runs/r1' && method === 'GET') {
    runFetchCount += 1
    return ok(snapshot)
  }
  if (url === '/api/v1/sessions/s1/runs' && method === 'POST') {
    return ok({ ...startResult, workspace_warning: workspaceWarning })
  }
  if (url === '/api/v1/runs/r1/cancel' && method === 'POST') {
    if (cancelStatus !== 200) {
      return fail(cancelStatus, { code: 'internal_error', message: '停止服务异常' })
    }
    snapshot = makeSnapshot('cancelled')
    return ok(snapshot)
  }
  if (url.startsWith('/api/v1/approvals/') && method === 'POST') {
    if (decisionStatus === 409) {
      return fail(409, { code: 'approval_already_decided', message: '审批已处理' })
    }
    return ok({
      id: 'ap1',
      run_id: 'r1',
      tool_call_id: 'tc1',
      status: 'approved',
      requested_at: '2026-11-18T10:00:00Z',
      decision: { approved: true, status: 'approved' },
    })
  }
  return fail(404, { code: 'not_found', message: `未模拟的路径：${url}` })
})

beforeEach(() => {
  FakeEventSource.instances = []
  messages = [userMessage, streamingMessage]
  snapshot = makeSnapshot('running')
  runFetchCount = 0
  decisionStatus = 200
  cancelStatus = 200
  workspaceWarning = false
  calls = []
  useRunStore.setState({
    deltas: {},
    lastSeq: {},
    seenEventKeys: new Set<string>(),
    connection: null,
    activeBySession: {},
    drafts: {},
    timelineCollapsed: false,
  })
  vi.stubGlobal('EventSource', FakeEventSource)
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('ChatPanel', () => {
  it('Markdown 渲染标题与代码块，复制按钮写入剪贴板', async () => {
    messages = [
      userMessage,
      { ...streamingMessage, status: 'completed', content: '# 标题\n\n```ts\nconst answer = 42\n```' },
    ]
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    renderWithProviders(<ChatPanel session={session} />)
    expect(await screen.findByRole('heading', { name: '标题', level: 1 })).toBeVisible()
    expect(document.querySelector('pre')?.textContent).toContain('const answer = 42')
    await userEvent.click(screen.getByRole('button', { name: '复制' }))
    expect(writeText).toHaveBeenCalledWith(expect.stringContaining('const answer = 42'))
  })

  it('Enter 发送消息并清空草稿', async () => {
    messages = [userMessage, { ...streamingMessage, status: 'completed', content: '回答完毕' }]
    renderWithProviders(<ChatPanel session={session} />)
    const textarea = await screen.findByPlaceholderText('输入消息')
    await userEvent.type(textarea, '新的任务{Enter}')
    await waitFor(() => {
      const call = calls.find((c) => c.url === '/api/v1/sessions/s1/runs' && c.method === 'POST')
      expect(call?.body).toEqual({ content: '新的任务' })
    })
    await waitFor(() => expect(textarea).toHaveValue(''))
  })

  it('Shift+Enter 换行不发送', async () => {
    messages = [userMessage, { ...streamingMessage, status: 'completed', content: '回答完毕' }]
    renderWithProviders(<ChatPanel session={session} />)
    const textarea = await screen.findByPlaceholderText('输入消息')
    await userEvent.type(textarea, '第一行{Shift>}{Enter}{/Shift}第二行')
    expect(textarea).toHaveValue('第一行\n第二行')
    expect(calls.some((c) => c.url === '/api/v1/sessions/s1/runs' && c.method === 'POST')).toBe(false)
  })

  it('活跃 Run 禁用发送，停止需二次确认并立即刷新快照', async () => {
    renderWithProviders(<ChatPanel session={session} />)
    await screen.findByText('运行中')
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: '停止' }))
    expect(await screen.findByText('停止当前任务')).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: '确认停止' }))
    await waitFor(() =>
      expect(calls.some((c) => c.url === '/api/v1/runs/r1/cancel' && c.method === 'POST')).toBe(true),
    )
    expect(await screen.findByText('已取消')).toBeVisible()
    expect(screen.queryByRole('button', { name: '停止' })).not.toBeInTheDocument()
  })

  it('刷新后已取消 Run 恢复状态标签与助手消息文本', async () => {
    // 刷新恢复：消息已刷盘为 cancelled，不再是 streaming，仍须找回所属 Run。
    messages = [
      userMessage,
      { ...streamingMessage, status: 'cancelled', content: '片段[0001] 片段[0002]' },
    ]
    snapshot = makeSnapshot('cancelled')
    renderWithProviders(<ChatPanel session={session} />)
    expect(await screen.findByText('已取消')).toBeVisible()
    expect(screen.getByText('片段[0001] 片段[0002]')).toBeVisible()
    expect(screen.queryByRole('button', { name: '停止' })).not.toBeInTheDocument()
    expect(screen.queryByText('连接中')).not.toBeInTheDocument()
  })

  it('停止弹窗取消不发送取消请求', async () => {
    renderWithProviders(<ChatPanel session={session} />)
    await userEvent.click(await screen.findByRole('button', { name: '停止' }))
    await screen.findByText('停止当前任务')
    await userEvent.click(screen.getByRole('button', { name: '取消' }))
    expect(calls.some((c) => c.url === '/api/v1/runs/r1/cancel')).toBe(false)
  })

  it('切换会话保留各自草稿', async () => {
    const view = renderWithProviders(<ChatPanel key="s1" session={session} />)
    await userEvent.type(await screen.findByPlaceholderText('输入消息'), '草稿内容')
    view.rerender(<ChatPanel key="s2" session={session2} />)
    await waitFor(() => expect(screen.getByPlaceholderText('输入消息')).toHaveValue(''))
    view.rerender(<ChatPanel key="s1" session={session} />)
    await waitFor(() => expect(screen.getByPlaceholderText('输入消息')).toHaveValue('草稿内容'))
  })

  it('连接状态显示连接中/已连接/重连中，onerror 不误判 Run 失败', async () => {
    renderWithProviders(<ChatPanel session={session} />)
    expect(await screen.findByText('连接中')).toBeVisible()
    const source = FakeEventSource.instances.at(-1)
    expect(source?.url).toBe('/api/v1/runs/r1/events?after=5')
    act(() => source?.emitOpen())
    expect(screen.getByText('已连接')).toBeVisible()
    act(() => source?.emitError())
    expect(screen.getByText('重连中')).toBeVisible()
    expect(screen.getByText('运行中')).toBeVisible()
    expect(screen.queryByText('失败')).not.toBeInTheDocument()
  })

  it('SSE 增量按 seq 去重，终态关闭连接并由权威快照接管', async () => {
    renderWithProviders(<ChatPanel session={session} />)
    await screen.findByText('连接中')
    const source = FakeEventSource.instances.at(-1)
    const delta = envelope({
      seq: 6,
      type: 'message.delta',
      payload: { message_id: 'm2', delta: '你好' },
    })
    act(() => {
      source?.emitMessage(delta)
      source?.emitMessage(delta)
    })
    expect(screen.getAllByText('你好')).toHaveLength(1)
    messages = [userMessage, { ...streamingMessage, status: 'completed', content: '你好，完整回答' }]
    snapshot = makeSnapshot('succeeded')
    act(() => source?.emitMessage(envelope({ seq: 7, type: 'run.completed' })))
    expect(await screen.findByText('你好，完整回答')).toBeVisible()
    expect(source?.closed).toBe(true)
    expect(await screen.findByText('已完成')).toBeVisible()
    expect(screen.queryByText('连接中')).not.toBeInTheDocument()
    expect(screen.queryByText('已连接')).not.toBeInTheDocument()
  })

  it('审批 409 后刷新 Run 快照并恢复可交互', async () => {
    const toolCall: ToolCallView = {
      id: 'tc1',
      run_id: 'r1',
      approval_id: 'ap1',
      name: 'shell',
      arguments_json: { command: 'rm ./generated.tmp' },
      status: 'waiting_approval',
      result_json: null,
      created_at: '2026-11-18T10:00:00Z',
    }
    const approval: ApprovalView = {
      id: 'ap1',
      run_id: 'r1',
      tool_call_id: 'tc1',
      status: 'pending',
      requested_payload_json: { command: 'rm ./generated.tmp', dir: '/tmp/ws', timeout_seconds: 30 },
      requested_at: '2026-11-18T10:00:00Z',
    }
    snapshot = { ...makeSnapshot('waiting_approval'), tool_calls: [toolCall], approvals: [approval] }
    decisionStatus = 409
    renderWithProviders(<ChatPanel session={session} />)
    await screen.findByRole('button', { name: '批准' })
    const before = runFetchCount
    await userEvent.click(screen.getByRole('button', { name: '批准' }))
    await waitFor(() => expect(runFetchCount).toBeGreaterThan(before))
    const call = calls.find((c) => c.url === '/api/v1/approvals/ap1/decision')
    expect(call?.body).toEqual({ approved: true, reason: '' })
    expect(await screen.findByText('审批已处理')).toBeVisible()
    expect(screen.getByRole('button', { name: '批准' })).toBeEnabled()
  })

  it('waiting_approval 工具卡渲染等待审批状态', async () => {
    const toolCall: ToolCallView = {
      id: 'tc1',
      run_id: 'r1',
      approval_id: 'ap1',
      name: 'shell',
      arguments_json: { command: 'ls' },
      status: 'waiting_approval',
      result_json: null,
      created_at: '2026-11-18T10:00:00Z',
    }
    snapshot = { ...makeSnapshot('waiting_approval'), tool_calls: [toolCall] }
    renderWithProviders(<ChatPanel session={session} />)
    expect(await screen.findByText('shell')).toBeVisible()
    // Run 状态 Tag 与工具卡状态 Tag 各一个。
    expect(screen.getAllByText('等待审批')).toHaveLength(2)
  })

  it('未知工具状态兜底展示原始值而不崩溃', async () => {
    const toolCall: ToolCallView = {
      id: 'tc1',
      run_id: 'r1',
      name: 'file_write',
      arguments_json: { path: 'a.txt' },
      status: 'mystery' as ToolCallStatus,
      result_json: null,
      created_at: '2026-11-18T10:00:00Z',
    }
    snapshot = { ...makeSnapshot('running'), tool_calls: [toolCall] }
    renderWithProviders(<ChatPanel session={session} />)
    expect(await screen.findByText('file_write')).toBeVisible()
    expect(screen.getByText('mystery')).toBeVisible()
  })

  it('streaming 增量与已刷盘 content 重叠去重，不重复拼接', async () => {
    renderWithProviders(<ChatPanel session={session} />)
    await screen.findByText('连接中')
    const source = FakeEventSource.instances.at(-1)
    act(() =>
      source?.emitMessage(
        envelope({ seq: 6, type: 'message.delta', payload: { message_id: 'm2', delta: '你好，世界' } }),
      ),
    )
    expect(screen.getAllByText('你好，世界')).toHaveLength(1)
    // 服务端已把前缀刷盘：重取后 content 与本地增量重叠，只能显示一份。
    // 用新增的标记消息确认重取结果已渲染，避免断言跑在重取完成之前。
    const marker: MessageView = {
      ...userMessage,
      id: 'm5',
      session_seq: 5,
      status: 'completed',
      content: '刷新标记一',
    }
    messages = [userMessage, { ...streamingMessage, content: '你好，' }, marker]
    act(() => source?.emitMessage(envelope({ seq: 7, type: 'message.started' })))
    await screen.findByText('刷新标记一')
    expect(screen.getAllByText('你好，世界')).toHaveLength(1)
    // content 与增量无重叠时全量保留，不得误删文本。
    messages = [
      userMessage,
      { ...streamingMessage, content: '前置文本' },
      { ...marker, id: 'm6', session_seq: 6, content: '刷新标记二' },
    ]
    act(() => source?.emitMessage(envelope({ seq: 8, type: 'message.started' })))
    await screen.findByText('刷新标记二')
    expect(screen.getAllByText('前置文本你好，世界')).toHaveLength(1)
  })

  it('停止失败提示错误并保留弹窗可重试', async () => {
    cancelStatus = 500
    renderWithProviders(<ChatPanel session={session} />)
    await userEvent.click(await screen.findByRole('button', { name: '停止' }))
    await userEvent.click(await screen.findByRole('button', { name: '确认停止' }))
    // antd message 入场动画首帧在 jsdom 中不可见，等待动画推进后再断言。
    const notice = await screen.findByText('停止服务异常')
    await waitFor(() => expect(notice).toBeVisible())
    expect(screen.getByText('停止当前任务')).toBeVisible()
    expect(screen.getByRole('button', { name: '确认停止' })).toBeEnabled()
    cancelStatus = 200
    await userEvent.click(screen.getByRole('button', { name: '确认停止' }))
    expect(await screen.findByText('已取消')).toBeVisible()
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: '停止' })).not.toBeInTheDocument(),
    )
  })

  it('发送后 workspace_warning 提示并发修改风险', async () => {
    messages = [userMessage, { ...streamingMessage, status: 'completed', content: '回答完毕' }]
    workspaceWarning = true
    renderWithProviders(<ChatPanel session={session} />)
    await userEvent.type(await screen.findByPlaceholderText('输入消息'), '新的任务{Enter}')
    expect(
      await screen.findByText('其他会话正在此目录执行任务，注意并发修改风险'),
    ).toBeVisible()
  })

  it('SSE 永久关闭时失效重取快照并清除连接文案', async () => {
    renderWithProviders(<ChatPanel session={session} />)
    await screen.findByText('连接中')
    const source = FakeEventSource.instances.at(-1)
    act(() => source?.emitOpen())
    const before = runFetchCount
    act(() => {
      if (source) {
        source.readyState = FakeEventSource.CLOSED
      }
      source?.emitError()
    })
    await waitFor(() => expect(runFetchCount).toBeGreaterThan(before))
    expect(source?.closed).toBe(true)
    expect(screen.queryByText('重连中')).not.toBeInTheDocument()
    expect(screen.queryByText('已连接')).not.toBeInTheDocument()
  })
})
