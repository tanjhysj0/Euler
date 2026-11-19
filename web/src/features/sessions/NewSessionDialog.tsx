import { Alert, Button, Form, Input, Modal, Select, Space, Typography } from 'antd'
import { useEffect, useState } from 'react'
import { APIError } from '../../api/client'
import { useDirectoryListing, useSessionList } from '../../api/queries'
import type { CreateSessionInput, ModelProfile } from '../../api/types'

interface NewSessionDialogProps {
  open: boolean
  profiles: ModelProfile[]
  onCreate?: (input: CreateSessionInput) => Promise<{ id: string }>
  onCancel?: () => void
  workspaceWarning?: boolean
}

// NewSessionDialog：选择模型、输入绝对路径或浏览本机目录；路径留空时后端
// 自动创建默认工作区。路径变化后查询该目录上的活跃 Run 并展示并发警告。
export default function NewSessionDialog({
  open,
  profiles,
  onCreate,
  onCancel,
  workspaceWarning,
}: NewSessionDialogProps) {
  const [form] = Form.useForm<CreateSessionInput>()
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [browsing, setBrowsing] = useState(false)
  const [browsePath, setBrowsePath] = useState('')
  const path = Form.useWatch('path', form) ?? ''
  const trimmedPath = path.trim()
  // 配置列表可能晚于对话框打开才加载完成；initialValues 不是响应式的，
  // 加载完成后若用户尚未选择则自动选中第一个配置。
  const defaultProfileId = profiles[0]?.id
  useEffect(() => {
    if (defaultProfileId && form.getFieldValue('profile_id') === undefined) {
      form.setFieldValue('profile_id', defaultProfileId)
    }
  }, [defaultProfileId, form])

  const activeSessions = useSessionList(
    { workspacePath: trimmedPath, activeRun: true },
    open && trimmedPath !== '',
  )
  const listing = useDirectoryListing(browsePath, open && browsing)
  const showWarning = workspaceWarning || (activeSessions.data?.length ?? 0) > 0

  async function handleFinish(values: CreateSessionInput) {
    if (!onCreate) {
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      await onCreate({ profile_id: values.profile_id, path: (values.path ?? '').trim() })
      form.resetFields()
      setBrowsing(false)
    } catch (err) {
      setError(err instanceof APIError ? err.message : '创建会话失败')
    } finally {
      setSubmitting(false)
    }
  }

  function choosePath(value: string) {
    form.setFieldValue('path', value)
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
        initialValues={{ profile_id: profiles[0]?.id, path: '' }}
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
        <Form.Item name="path" label="工作区目录">
          <Input placeholder="绝对路径，例如 /Users/me/project" allowClear />
        </Form.Item>
      </Form>
      <Space orientation="vertical" size="small" style={{ width: '100%' }}>
        {trimmedPath === '' && (
          <Typography.Text type="secondary">
            目录留空将自动创建默认工作区
          </Typography.Text>
        )}
        {showWarning && (
          <Alert
            type="warning"
            showIcon
            title="其他会话正在此目录执行任务，继续创建可能产生并发修改"
          />
        )}
        {error && <Alert type="error" showIcon title={error} />}
        <Button onClick={() => setBrowsing((value) => !value)}>
          {browsing ? '收起目录浏览' : '浏览目录'}
        </Button>
        {browsing && listing.data && (
          <div>
            <Space style={{ marginBottom: 8 }}>
              <Typography.Text code>{listing.data.current_path}</Typography.Text>
              <Button
                size="small"
                disabled={listing.data.parent_path === null}
                onClick={() => setBrowsePath(listing.data?.parent_path ?? '')}
              >
                上级目录
              </Button>
              <Button size="small" onClick={() => choosePath(listing.data?.current_path ?? '')}>
                选择此目录
              </Button>
            </Space>
            {listing.data.directories.map((directory) => (
              <Space key={directory.path} style={{ display: 'flex', marginBottom: 4 }}>
                <Button type="link" size="small" onClick={() => setBrowsePath(directory.path)}>
                  {directory.name}
                </Button>
                <Button size="small" onClick={() => choosePath(directory.path)}>
                  选择
                </Button>
              </Space>
            ))}
          </div>
        )}
        {browsing && listing.isError && (
          <Alert type="error" showIcon title="目录不可访问，请检查路径" />
        )}
      </Space>
    </Modal>
  )
}
