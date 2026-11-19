import { Alert, Button, Form, Input, Modal, Radio, Space, Table, Typography } from 'antd'
import { useState } from 'react'
import { APIError } from '../../api/client'
import { useCreateModelProfile, useModelProfiles } from '../../api/queries'
import type { CreateModelProfileInput, ModelProfile } from '../../api/types'

// ModelProfiles 设置页：模型配置列表与创建表单。API Key 只写入不回显；
// 环境变量模式引用已有环境变量，不显示 API Key 输入。
export default function ModelProfiles() {
  const profilesQuery = useModelProfiles()
  const createProfile = useCreateModelProfile()
  const [open, setOpen] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [form] = Form.useForm<CreateModelProfileInput>()
  const secretKind = Form.useWatch('secret_kind', form) ?? 'keychain'

  async function handleSave(values: CreateModelProfileInput) {
    setError(null)
    try {
      await createProfile.mutateAsync({
        name: values.name,
        base_url: values.base_url,
        model: values.model,
        secret_kind: values.secret_kind,
        secret_ref: values.secret_kind === 'environment' ? values.secret_ref : '',
        api_key: values.secret_kind === 'keychain' ? values.api_key : undefined,
      })
      setOpen(false)
      form.resetFields()
    } catch (err) {
      setError(err instanceof APIError ? err.message : '创建模型配置失败')
    }
  }

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Typography.Title level={4} style={{ margin: 0 }}>
        模型设置
      </Typography.Title>
      <Button type="primary" onClick={() => setOpen(true)}>
        新建模型配置
      </Button>
      <Table<ModelProfile>
        rowKey="id"
        loading={profilesQuery.isPending}
        dataSource={profilesQuery.data ?? []}
        pagination={false}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: 'Base URL', dataIndex: 'base_url' },
          { title: '模型', dataIndex: 'model' },
          {
            title: '密钥方式',
            render: (_, profile) =>
              profile.secret_kind === 'environment'
                ? `环境变量 ${profile.secret_ref}`
                : '钥匙串',
          },
        ]}
      />
      <Modal
        open={open}
        title="新建模型配置"
        okText="保存"
        cancelText="取消"
        confirmLoading={createProfile.isPending}
        onOk={() => form.submit()}
        onCancel={() => setOpen(false)}
      >
        <Form
          form={form}
          layout="vertical"
          initialValues={{ secret_kind: 'keychain' }}
          onFinish={handleSave}
        >
          <Form.Item
            name="name"
            label="名称"
            rules={[{ required: true, message: '请输入名称' }]}
          >
            <Input placeholder="例如 OpenAI 官方" />
          </Form.Item>
          <Form.Item
            name="base_url"
            label="Base URL"
            rules={[{ required: true, message: '请输入 Base URL' }]}
          >
            <Input placeholder="https://api.openai.com/v1" />
          </Form.Item>
          <Form.Item
            name="model"
            label="模型"
            rules={[{ required: true, message: '请输入模型名' }]}
          >
            <Input placeholder="例如 gpt-4o" />
          </Form.Item>
          <Form.Item name="secret_kind" label="密钥方式">
            <Radio.Group>
              <Radio value="keychain">钥匙串</Radio>
              <Radio value="environment">环境变量</Radio>
            </Radio.Group>
          </Form.Item>
          {secretKind === 'keychain' && (
            <Form.Item
              name="api_key"
              label="API Key"
              extra="只写入，不回显"
              rules={[{ required: true, message: '请输入 API Key' }]}
            >
              <Input.Password placeholder="写入本机钥匙串" autoComplete="new-password" />
            </Form.Item>
          )}
          {secretKind === 'environment' && (
            <Form.Item
              name="secret_ref"
              label="环境变量名"
              rules={[{ required: true, message: '请输入环境变量名' }]}
            >
              <Input placeholder="例如 OPENAI_API_KEY" />
            </Form.Item>
          )}
        </Form>
        {error && <Alert type="error" showIcon title={error} />}
      </Modal>
    </Space>
  )
}
