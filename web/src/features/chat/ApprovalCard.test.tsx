import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { APIError } from '../../api/client'
import type { ApprovalView } from '../../api/types'
import ApprovalCard from './ApprovalCard'

const shellApproval: ApprovalView = {
  id: 'approval-shell',
  run_id: 'r1',
  tool_call_id: 'tc-shell',
  status: 'pending',
  requested_payload_json: {
    command: 'rm ./generated.tmp',
    dir: '/tmp/ws',
    timeout_seconds: 30,
  },
  requested_at: '2026-11-18T10:00:00Z',
}

const fileApproval: ApprovalView = {
  id: 'approval-file',
  run_id: 'r1',
  tool_call_id: 'tc-file',
  status: 'pending',
  requested_payload_json: { path: 'note.txt', diff: '@@ -1 +1 @@\n-old\n+new' },
  requested_at: '2026-11-18T10:00:00Z',
}

describe('ApprovalCard', () => {
  it('Shell 审批显示完整命令并允许拒绝', async () => {
    const onDecide = vi.fn().mockResolvedValue(undefined)
    render(<ApprovalCard approval={shellApproval} onDecide={onDecide} />)
    expect(screen.getByText('rm ./generated.tmp')).toBeVisible()
    expect(screen.getByText(/可访问当前 OS 用户有权限的其他资源/)).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: '拒绝' }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledWith(shellApproval.id, false, ''))
  })

  it('Shell 审批展示目录与超时', () => {
    render(<ApprovalCard approval={shellApproval} onDecide={vi.fn()} />)
    expect(screen.getByText(/\/tmp\/ws/)).toBeVisible()
    expect(screen.getByText(/30 秒/)).toBeVisible()
  })

  it('文件审批展示路径与 diff，批准提交空原因', async () => {
    const onDecide = vi.fn().mockResolvedValue(undefined)
    render(<ApprovalCard approval={fileApproval} name="file_write" onDecide={onDecide} />)
    expect(screen.getByText('file_write')).toBeVisible()
    expect(screen.getByText('note.txt')).toBeVisible()
    expect(screen.getByText(/-old/)).toBeVisible()
    expect(screen.getByText(/\+new/)).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: '批准' }))
    await waitFor(() => expect(onDecide).toHaveBeenCalledWith(fileApproval.id, true, ''))
  })

  it('file_delete 审批只展示路径', () => {
    const approval: ApprovalView = {
      ...fileApproval,
      id: 'approval-delete',
      requested_payload_json: { path: 'old.txt' },
    }
    render(<ApprovalCard approval={approval} name="file_delete" onDecide={vi.fn()} />)
    expect(screen.getByText('old.txt')).toBeVisible()
    expect(screen.queryByText(/@@/)).not.toBeInTheDocument()
  })

  it('等待决策期间禁止重复提交', async () => {
    let resolveDecision: () => void = () => {}
    const onDecide = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          resolveDecision = resolve
        }),
    )
    render(<ApprovalCard approval={shellApproval} onDecide={onDecide} />)
    await userEvent.click(screen.getByRole('button', { name: '批准' }))
    await waitFor(() => expect(screen.getByRole('button', { name: '批准' })).toBeDisabled())
    expect(screen.getByRole('button', { name: '拒绝' })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: '拒绝' }))
    expect(onDecide).toHaveBeenCalledTimes(1)
    resolveDecision()
  })

  it('拒绝可以填写原因', async () => {
    const onDecide = vi.fn().mockResolvedValue(undefined)
    render(<ApprovalCard approval={shellApproval} onDecide={onDecide} />)
    await userEvent.type(screen.getByPlaceholderText('拒绝原因（可选）'), '危险命令')
    await userEvent.click(screen.getByRole('button', { name: '拒绝' }))
    await waitFor(() =>
      expect(onDecide).toHaveBeenCalledWith(shellApproval.id, false, '危险命令'),
    )
  })

  it('409 后恢复可交互，快照刷新后展示已决策状态', async () => {
    const onDecide = vi
      .fn()
      .mockRejectedValue(new APIError(409, { code: 'approval_already_decided', message: '审批已处理' }))
    const view = render(<ApprovalCard approval={shellApproval} onDecide={onDecide} />)
    await userEvent.click(screen.getByRole('button', { name: '批准' }))
    await waitFor(() => expect(screen.getByRole('button', { name: '批准' })).toBeEnabled())
    view.rerender(
      <ApprovalCard
        approval={{
          ...shellApproval,
          status: 'approved',
          decision_payload_json: { approved: true, status: 'approved' },
        }}
        onDecide={onDecide}
      />,
    )
    expect(screen.queryByRole('button', { name: '批准' })).not.toBeInTheDocument()
    expect(screen.getByText('已批准')).toBeVisible()
  })

  it('缺少 name 时兜底为文件操作，不用 diff 猜测写入或删除', () => {
    const approval: ApprovalView = {
      ...fileApproval,
      id: 'approval-unknown',
      requested_payload_json: { path: 'note.txt' },
    }
    const view = render(<ApprovalCard approval={approval} onDecide={vi.fn()} />)
    expect(screen.getByText('文件操作')).toBeVisible()
    expect(screen.queryByText('文件删除')).not.toBeInTheDocument()
    view.rerender(
      <ApprovalCard
        approval={{ ...approval, requested_payload_json: { path: 'note.txt', diff: '@@' } }}
        onDecide={vi.fn()}
      />,
    )
    expect(screen.getByText('文件操作')).toBeVisible()
    expect(screen.queryByText('文件写入')).not.toBeInTheDocument()
  })
})
