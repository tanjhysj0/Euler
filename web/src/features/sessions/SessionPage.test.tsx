import { screen } from '@testing-library/react'
import { Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SessionView } from '../../api/types'
import { renderWithProviders } from '../../test/utils'
import SessionPage from './SessionPage'

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

let sessionStatus = 200

const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
  const url = String(input)
  if (url === '/api/v1/sessions/s1') {
    if (sessionStatus === 404) {
      return {
        ok: false,
        status: 404,
        json: async () => ({ code: 'session_not_found', message: '会话不存在' }),
      } as Response
    }
    return { ok: true, status: 200, json: async () => session } as Response
  }
  if (url.startsWith('/api/v1/sessions/s1/messages')) {
    return { ok: true, status: 200, json: async () => ({ messages: [], next_after: 0 }) } as Response
  }
  return { ok: false, status: 404, json: async () => ({ code: 'not_found', message: url }) } as Response
})

function renderAt(path: string) {
  return renderWithProviders(
    <Routes>
      <Route path="sessions/:id" element={<SessionPage />} />
    </Routes>,
    [path],
  )
}

beforeEach(() => {
  sessionStatus = 200
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('SessionPage', () => {
  it('会话不存在时展示 Empty', async () => {
    sessionStatus = 404
    renderAt('/sessions/s1')
    expect(await screen.findByText('会话不存在')).toBeVisible()
  })

  it('加载成功后渲染会话头部与 ChatPanel 输入区', async () => {
    renderAt('/sessions/s1')
    expect(await screen.findByText('会话一')).toBeVisible()
    expect(screen.getByText('/tmp/ws')).toBeVisible()
    expect(await screen.findByPlaceholderText('输入消息')).toBeVisible()
  })
})
