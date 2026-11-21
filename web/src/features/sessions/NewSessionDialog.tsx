import { Alert, Form, Modal, Select, Typography } from 'antd'
import { useEffect, useState } from 'react'
import { APIError } from '../../api/client'
import type { CreateSessionInput, ModelProfile } from '../../api/types'

interface NewSessionDialogProps {
  open: boolean
  profiles: ModelProfile[]
  onCreate?: (input: CreateSessionInput) => Promise<{ id: string }>
  onCancel?: () => void
}

// NewSessionDialog 只选择模型配置；工作区目录由后端按会话自动分配，
// 创建后可在右侧文件区查看该目录下的产物。
export default function NewSessionDialog({
  open,
  profiles,
  onCreate,
  onCancel,
}: NewSessionDialogProps) {
  const [form] = Form.useForm<CreateSessionInput>()
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // 配置列表可能晚于对话框打开才加载完成；initialValues 不是响应式的，
  // 加载完成后若用户尚未选择则自动选中第一个配置。
  const defaultProfileId = profiles[0]?.id
  useEffect(() => {
    if (defaultProfileId && form.getFieldValue('profile_id') === undefined) {
      form.setFieldValue('profile_id', defaultProfileId)
    }
  }, [defaultProfileId, form])

  async function handleFinish(values: CreateSessionInput) {
    if (!onCreate) {
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      await onCreate({ profile_id: values.profile_id, path: '' })
      form.resetFields()
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
      onOk={() => form.submit()}
      onCancel={onCancel}
    >
      <Form
        form={form}
        layout="vertical"
        initialValues={{ profile_id: profiles[0]?.id }}
        onFinish={handleFinish}
      >
        <Form.Item
          name="profile_id"
          label="模型"
          rules={[{ required: true, message: '请选择模型配置' }]}
        >
          <Select
            placeholder="选择模型配置"
            options={profiles.map((profile) => ({
              value: profile.id,
              label: profile.name,
            }))}
          />
        </Form.Item>
      </Form>
      <Typography.Text type="secondary">
        系统会自动分配独立工作区，创建后可在右侧文件区查看产物。
      </Typography.Text>
      {error && <Alert type="error" showIcon title={error} style={{ marginTop: 12 }} />}
    </Modal>
  )
}
