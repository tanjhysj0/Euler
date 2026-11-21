import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { FileNode } from '../../api/types'
import { renderWithProviders } from '../../test/utils'
import FilePanel from './FilePanel'

const modTime = '2026-11-18T10:00:00Z'

const nodes: FileNode[] = [
  { path: 'docs', type: 'dir', size: 0, mod_time: modTime },
  { path: 'docs/guide.md', type: 'file', size: 12, mod_time: modTime },
  { path: 'note.txt', type: 'file', size: 11, mod_time: modTime },
  { path: 'pic.png', type: 'file', size: 100, mod_time: modTime },
  { path: 'blob.bin', type: 'file', size: 6, mod_time: modTime },
  { path: 'big.txt', type: 'file', size: 999999, mod_time: modTime },
]

function textResponse(body: string, contentType: string): Response {
  return {
    ok: true,
    status: 200,
    headers: new Headers({ 'content-type': contentType }),
    text: async () => body,
  } as Response
}

function errorResponse(status: number, code: string, message: string): Response {
  return { ok: false, status, json: async () => ({ code, message }) } as Response
}

const contents: Record<string, Response> = {
  'note.txt': textResponse('hello world', 'text/plain; charset=utf-8'),
  'docs/guide.md': textResponse('# 指南标题', 'text/markdown; charset=utf-8'),
  'blob.bin': textResponse('binary', 'application/octet-stream'),
}

const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
  const url = String(input)
  if (url === '/api/v1/workspaces/w1/files') {
    return { ok: true, status: 200, json: async () => nodes } as Response
  }
  const path = new URL(url, 'http://localhost').searchParams.get('path') ?? ''
  if (path === 'big.txt') {
    return errorResponse(413, 'file_too_large', '文件超出预览大小限制')
  }
  if (contents[path]) {
    return contents[path]
  }
  return errorResponse(404, 'not_found', '工作区文件不存在')
})

beforeEach(() => {
  fetchMock.mockClear()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('FilePanel', () => {
  it('展示目录树并在选择文本文件后用代码视图预览', async () => {
    renderWithProviders(<FilePanel workspaceId="w1" />)
    expect(await screen.findByText('docs')).toBeVisible()
    fireEvent.click(await screen.findByText('note.txt'))
    await waitFor(() => expect(screen.getByText('hello world')).toBeVisible())
    expect(screen.getByText('hello world').tagName).toBe('PRE')
  })

  it('点击目录收起子项，再次点击展开子项', async () => {
    renderWithProviders(<FilePanel workspaceId="w1" />)
    expect(await screen.findByText('guide.md')).toBeVisible()
    fireEvent.click(screen.getByText('docs'))
    await waitFor(() => expect(screen.queryByText('guide.md')).not.toBeInTheDocument())
    fireEvent.click(screen.getByText('docs'))
    // antd Tree 展开动画在 jsdom 中不会推进，断言节点重新挂载即可。
    await waitFor(() => expect(screen.queryByText('guide.md')).toBeInTheDocument())
  })

  it('超出预览大小限制时展示错误与下载链接', async () => {
    renderWithProviders(<FilePanel workspaceId="w1" />)
    fireEvent.click(await screen.findByText('big.txt'))
    expect(await screen.findByText('文件超出预览大小限制')).toBeVisible()
    const link = screen.getByText('下载').closest('a')
    expect(link?.getAttribute('href')).toContain('download=true')
    expect(link?.getAttribute('href')).toContain('path=big.txt')
  })

  it('图片使用受保护 content API URL 而不是本地路径', async () => {
    renderWithProviders(<FilePanel workspaceId="w1" />)
    fireEvent.click(await screen.findByText('pic.png'))
    const image = await screen.findByAltText('pic.png')
    const src = image.getAttribute('src') ?? ''
    expect(src).toBe('/api/v1/workspaces/w1/files/content?path=pic.png')
    expect(src).not.toContain('file://')
    const fetchedContent = fetchMock.mock.calls.some(([input]) =>
      String(input).includes('path=pic.png'),
    )
    expect(fetchedContent).toBe(false)
  })

  it('Markdown 文件使用与消息一致的渲染器', async () => {
    renderWithProviders(<FilePanel workspaceId="w1" />)
    fireEvent.click(await screen.findByText('guide.md'))
    expect(await screen.findByRole('heading', { name: '指南标题' })).toBeVisible()
  })

  it('二进制文件只提供下载链接', async () => {
    renderWithProviders(<FilePanel workspaceId="w1" />)
    fireEvent.click(await screen.findByText('blob.bin'))
    await waitFor(() => expect(screen.getByText('下载')).toBeVisible())
    const link = screen.getByText('下载').closest('a')
    expect(link?.getAttribute('href')).toContain('download=true')
    expect(screen.queryByText('binary')).toBeNull()
  })
})
