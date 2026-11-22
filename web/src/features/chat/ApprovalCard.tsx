import { Alert, Button, Card, Input, Space, Tag, Typography } from 'antd'
import { useState } from 'react'
import { APIError } from '../../api/client'
import type {
  ApprovalView,
  FileApprovalPayload,
  ShellApprovalPayload,
} from '../../api/types'

interface ApprovalCardProps {
  approval: ApprovalView
  name?: string
  onDecide: (approvalId: string, approved: boolean, reason: string) => Promise<void>
}

const statusLabels: Record<string, { label: string; color: string }> = {
  pending: { label: '等待审批', color: 'processing' },
  approved: { label: '已批准', color: 'success' },
  rejected: { label: '已拒绝', color: 'warning' },
  cancelled: { label: '已取消', color: 'default' },
}

// ApprovalCard 展示待审批工具的完整请求：文件类显示路径与 diff，Shell 显示
// 完整命令、目录、超时与 OS 权限警告。等待决策期间禁止重复提交；拒绝可填原因。
export default function ApprovalCard({ approval, name, onDecide }: ApprovalCardProps) {
  const [submitting, setSubmitting] = useState(false)
  const [reason, setReason] = useState('')
  const [error, setError] = useState<string | null>(null)
  const payload = (approval.requested_payload_json ?? {}) as Partial<
    ShellApprovalPayload & FileApprovalPayload
  >
  const isShell = typeof payload.command === 'string'
  const status = statusLabels[approval.status] ?? statusLabels.pending
  const decision = approval.decision_payload_json as { reason?: string } | null | undefined
  // 缺少 name 时不根据 diff 猜测写入/删除（file_patch 也带 diff），统一兜底。
  const title = name ?? (isShell ? 'Shell 命令' : '文件操作')

  async function decide(approved: boolean) {
    if (submitting) {
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      await onDecide(approval.id, approved, approved ? '' : reason)
    } catch (err) {
      // 409（审批已处理）等错误由父组件刷新快照，这里恢复可交互。
      setError(err instanceof APIError ? err.message : '审批提交失败')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Card
      size="small"
      className="approval-card"
      title={title}
      extra={<Tag color={status.color}>{status.label}</Tag>}
    >
      <Space orientation="vertical" size="small" style={{ width: '100%' }}>
        {isShell ? (
          <>
            <pre style={{ margin: 0, whiteSpace: 'pre-wrap' }}>{payload.command}</pre>
            <Typography.Text type="secondary">目录：{payload.dir}</Typography.Text>
            <Typography.Text type="secondary">超时：{payload.timeout_seconds} 秒</Typography.Text>
            <Alert
              type="warning"
              showIcon
              title="Shell 命令不做沙箱隔离，可访问当前 OS 用户有权限的其他资源，请确认命令安全。"
            />
          </>
        ) : (
          <>
            <Typography.Text code>{payload.path}</Typography.Text>
            {typeof payload.diff === 'string' && (
              <pre style={{ margin: 0, whiteSpace: 'pre-wrap' }}>{payload.diff}</pre>
            )}
          </>
        )}
        {approval.status === 'pending' ? (
          <>
            <Input
              placeholder="拒绝原因（可选）"
              value={reason}
              disabled={submitting}
              onChange={(event) => setReason(event.target.value)}
            />
            <Space>
              <Button
                type="primary"
                autoInsertSpace={false}
                disabled={submitting}
                onClick={() => void decide(true)}
              >
                批准
              </Button>
              <Button
                danger
                autoInsertSpace={false}
                disabled={submitting}
                onClick={() => void decide(false)}
              >
                拒绝
              </Button>
            </Space>
          </>
        ) : (
          decision?.reason && (
            <Typography.Text type="secondary">原因：{decision.reason}</Typography.Text>
          )
        )}
        {error && <Alert type="error" showIcon title={error} />}
      </Space>
    </Card>
  )
}