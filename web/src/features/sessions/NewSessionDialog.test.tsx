import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { renderWithProviders } from '../../test/utils'
import NewSessionDialog from './NewSessionDialog'

describe('NewSessionDialog', () => {
  it('不再选择模型与目录，直接提交空路径由后端使用默认模型', async () => {
    const onCreate = vi.fn().mockResolvedValue({ id: 'session-1' })
    renderWithProviders(<NewSessionDialog open onCreate={onCreate} />)
    expect(screen.getByText(/系统会自动分配独立工作区并使用默认模型/)).toBeInTheDocument()
    expect(screen.queryByLabelText('工作区目录')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('模型')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '创建会话' }))
    await waitFor(() => expect(onCreate).toHaveBeenCalledWith({ path: '' }))
  })

  it('创建失败时展示后端错误信息', async () => {
    const onCreate = vi.fn().mockRejectedValue(new Error('创建失败'))
    renderWithProviders(<NewSessionDialog open onCreate={onCreate} />)
    await userEvent.click(screen.getByRole('button', { name: '创建会话' }))
    expect(await screen.findByText('创建会话失败')).toBeVisible()
  })
})
