import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { ModelProfile } from '../../api/types'
import { renderWithProviders } from '../../test/utils'
import NewSessionDialog from './NewSessionDialog'

const profile: ModelProfile = {
  id: 'profile-1',
  name: '测试模型',
  base_url: 'https://api.example.com/v1',
  model: 'gpt-test',
  secret_kind: 'environment',
  secret_ref: 'TEST_API_KEY',
}

describe('NewSessionDialog', () => {
  it('留空目录时明确提示自动创建工作区并提交空路径', async () => {
    const onCreate = vi.fn().mockResolvedValue({ id: 'session-1' })
    renderWithProviders(<NewSessionDialog open profiles={[profile]} onCreate={onCreate} />)
    expect(screen.getByText(/将自动创建默认工作区/)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '创建会话' }))
    await waitFor(() => expect(onCreate).toHaveBeenCalledWith(expect.objectContaining({ path: '' })))
  })

  it('模型配置晚于对话框加载时自动选中第一个配置', async () => {
    const onCreate = vi.fn().mockResolvedValue({ id: 'session-1' })
    const view = renderWithProviders(<NewSessionDialog open profiles={[]} onCreate={onCreate} />)
    view.rerender(<NewSessionDialog open profiles={[profile]} onCreate={onCreate} />)
    await userEvent.click(screen.getByRole('button', { name: '创建会话' }))
    await waitFor(() =>
      expect(onCreate).toHaveBeenCalledWith(
        expect.objectContaining({ profile_id: 'profile-1', path: '' }),
      ),
    )
  })

  it('已有目录存在活跃 Run 时展示并发修改警告', async () => {
    renderWithProviders(<NewSessionDialog open profiles={[profile]} workspaceWarning />)
    expect(screen.getByText(/其他会话正在此目录执行任务/)).toBeVisible()
  })
})
