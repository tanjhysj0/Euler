import { Alert, Modal, Typography } from 'antd'
import { useState } from 'react'
import { APIError } from '../../api/client'
import type { CreateSessionInput } from '../../api/types'

interface NewSessionDialogProps {
  open: boolean
  onCreate?: (input: CreateSessionInput) => Promise<{ id: string }>
  onCancel?: () => void
}

// NewSessionDialog 不再选择模型：后端使用默认模型创建工作区，
// 创建后可在输入区切换模型。工作区目录同样由后端自动分配。
export default function NewSessionDialog({
  open,
  onCreate,
  onCancel,
}: NewSessionDialogProps) {
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleCreate() {
    if (!onCreate) {
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      await onCreate({ path: '' })
    } catch (err) {
      setError(err instanceof APIError ? err.message : '创建会话失败')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Modal
      open={open}
      title="新建会话"
      okText="创建会话"
      cancelText="取消"
      confirmLoading={submitting}
      onOk={() => void handleCreate()}
      onCancel={onCancel}
    >
      <Typography.Text type="secondary">
        系统会自动分配独立工作区并使用默认模型，创建后可在输入区切换模型。
      </Typography.Text>
      {error && <Alert type="error" showIcon title={error} style={{ marginTop: 12 }} />}
    </Modal>
  )
}
