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
  it('不再提供目录输入，提交空路径由后端自动分配工作区', async () => {
    const onCreate = vi.fn().mockResolvedValue({ id: 'session-1' })
    renderWithProviders(<NewSessionDialog open profiles={[profile]} onCreate={onCreate} />)
    expect(screen.getByText(/系统会自动分配独立工作区/)).toBeInTheDocument()
    expect(screen.queryByLabelText('工作区目录')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '创建会话' }))
    await waitFor(() =>
      expect(onCreate).toHaveBeenCalledWith({ profile_id: 'profile-1', path: '' }),
    )
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
})
