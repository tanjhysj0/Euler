import {
  Alert,
  Button,
  Form,
  Input,
  Modal,
  Radio,
  Select,
  Space,
  Table,
  Tag,
  Typography,
  message,
} from 'antd'
import { useState, useRef } from 'react'
import { APIError } from '../../api/client'
import {
  useCreateModelProfile,
  useDeleteModelProfile,
  useListAvailableModels,
  useModelProfiles,
  useUpdateModelProfile,
} from '../../api/queries'
import type { ModelProfile } from '../../api/types'

interface ProfileFormValues {
  name: string
  base_url: string
  model: string
  secret_kind: 'keychain' | 'environment'
  secret_ref?: string
  api_key?: string
}

// ModelProfiles 设置页：模型配置的列表、创建、编辑与删除。
// 创建时先填写 Base URL 与 API Key，再从远程 /models 列表选择模型。
export default function ModelProfiles() {
  const profilesQuery = useModelProfiles()
  const createProfile = useCreateModelProfile()
  const updateProfile = useUpdateModelProfile()
  const deleteProfile = useDeleteModelProfile()
  const listModels = useListAvailableModels()
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<ModelProfile | null>(null)
  const [models, setModels] = useState<string[]>([])
  const [error, setError] = useState<string | null>(null)
  const [form] = Form.useForm<ProfileFormValues>()
  // 记录上一次拉取模型列表所用的参数，避免每次聚焦都重复请求。
  const fetchedKey = useRef('')
  const secretKind = Form.useWatch('secret_kind', form) ?? 'keychain'
  const modelValue = Form.useWatch('model', form)

  const modelOptions = Array.from(
    new Set([...models, modelValue].filter((item): item is string => Boolean(item))),
  ).map((item) => ({ value: item, label: item }))

  function openCreate() {
    setEditing(null)
    setModels([])
    setError(null)
    fetchedKey.current = ''
    form.resetFields()
    form.setFieldsValue({ secret_kind: 'keychain' })
    setOpen(true)
  }

  function openEdit(profile: ModelProfile) {
    setEditing(profile)
    setModels([profile.model])
    setError(null)
    fetchedKey.current = ''
    form.setFieldsValue({
      name: profile.name,
      base_url: profile.base_url,
      model: profile.model,
      secret_kind: profile.secret_kind,
      secret_ref: profile.secret_ref,
      api_key: '',
    })
    setOpen(true)
  }

  // handleFetchModels 在模型选择框获得焦点时拉取远程模型列表。
  // 必填项未就绪时静默返回，避免刚打开表单就报错。
  async function handleFetchModels() {
    const values = form.getFieldsValue()
    if (!values.base_url) {
      return
    }
    if (!editing && values.secret_kind === 'keychain' && !values.api_key) {
      return
    }
    const requestKey = [
      values.base_url,
      values.api_key ?? '',
      values.secret_kind,
      values.secret_ref ?? '',
      editing?.id ?? '',
    ].join('|')
    if (fetchedKey.current === requestKey) {
      return
    }
    fetchedKey.current = requestKey
    setError(null)
    try {
      const result = await listModels.mutateAsync({
        base_url: values.base_url,
        api_key: values.api_key || undefined,
        secret_kind: values.secret_kind,
        secret_ref:
          values.secret_kind === 'environment' ? values.secret_ref : undefined,
        profile_id: editing?.id,
      })
      setModels(result.models)
      if (result.models.length === 0) {
        fetchedKey.current = ''
        setError('该端点没有返回可用模型')
        return
      }
      if (!result.models.includes(form.getFieldValue('model') as string)) {
        form.setFieldValue('model', result.models[0])
      }
    } catch (err) {
      // 失败后允许下次聚焦重试。
      fetchedKey.current = ''
      setError(err instanceof APIError ? err.message : '获取模型列表失败')
    }
  }

  async function handleSave(values: ProfileFormValues) {
    setError(null)
    try {
      if (editing) {
        await updateProfile.mutateAsync({
          id: editing.id,
          input: {
            name: values.name,
            base_url: values.base_url,
            model: values.model,
            secret_ref:
              values.secret_kind === 'environment' ? values.secret_ref : undefined,
            api_key: values.api_key || undefined,
          },
        })
      } else {
        await createProfile.mutateAsync({
          name: values.name,
          base_url: values.base_url,
          model: values.model,
          secret_kind: values.secret_kind,
          secret_ref:
            values.secret_kind === 'environment' ? values.secret_ref ?? '' : '',
          api_key: values.secret_kind === 'keychain' ? values.api_key : undefined,
        })
      }
      setOpen(false)
      form.resetFields()
    } catch (err) {
      setError(err instanceof APIError ? err.message : '保存模型配置失败')
    }
  }

  async function handleDelete(profile: ModelProfile) {
    try {
      const result = await deleteProfile.mutateAsync(profile.id)
      if (result.rebound_sessions > 0) {
        message.success(
          `已删除 ${profile.name}，${result.rebound_sessions} 个会话已改用默认模型`,
        )
      } else {
        message.success(`已删除 ${profile.name}`)
      }
    } catch (err) {
      message.error(err instanceof APIError ? err.message : '删除模型配置失败')
    }
  }

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Typography.Title level={4} style={{ margin: 0 }}>
        模型设置
      </Typography.Title>
      <Button type="primary" onClick={openCreate}>
        新建模型配置
      </Button>
      <Table<ModelProfile>
        rowKey="id"
        loading={profilesQuery.isPending}
        dataSource={profilesQuery.data ?? []}
        pagination={false}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: 'Base URL', dataIndex: 'base_url', ellipsis: true },
          { title: '模型', dataIndex: 'model' },
          {
            title: '密钥',
            render: (_, profile) =>
              profile.secret_kind === 'environment' ? (
                <Tag>环境变量 {profile.secret_ref}</Tag>
              ) : (
                <Tag color="blue">钥匙串</Tag>
              ),
          },
          {
            title: '操作',
            width: 140,
            render: (_, profile) => (
              <Space size="small">
                <Button type="link" size="small" onClick={() => openEdit(profile)}>
                  编辑
                </Button>
                <Button
                  type="link"
                  size="small"
                  danger
                  loading={
                    deleteProfile.isPending && deleteProfile.variables === profile.id
                  }
                  onClick={() => void handleDelete(profile)}
                >
                  删除
                </Button>
              </Space>
            ),
          },
        ]}
      />
      <Modal
        open={open}
        title={editing ? '编辑模型配置' : '新建模型配置'}
        okText="保存"
        cancelText="取消"
        confirmLoading={createProfile.isPending || updateProfile.isPending}
        onOk={() => form.submit()}
        onCancel={() => setOpen(false)}
      >
        <Form form={form} layout="vertical" onFinish={handleSave}>
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
          <Form.Item name="secret_kind" label="密钥方式">
            <Radio.Group disabled={Boolean(editing)}>
              <Radio value="keychain">钥匙串</Radio>
              <Radio value="environment">环境变量</Radio>
            </Radio.Group>
          </Form.Item>
          {secretKind === 'keychain' && (
            <Form.Item
              name="api_key"
              label="API Key"
              extra={editing ? '留空则保留原密钥' : '只写入本机钥匙串，不回显'}
              rules={
                editing ? [] : [{ required: true, message: '请输入 API Key' }]
              }
            >
              <Input.Password placeholder="sk-..." autoComplete="new-password" />
            </Form.Item>
          )}
          {secretKind === 'environment' && (
            <Form.Item
              name="secret_ref"
              label="环境变量名"
              extra="读取后端进程中的同名环境变量"
              rules={[{ required: true, message: '请输入环境变量名' }]}
            >
              <Input placeholder="例如 OPENAI_API_KEY" />
            </Form.Item>
          )}
          <Form.Item
            name="model"
            label="模型"
            rules={[{ required: true, message: '请选择模型' }]}
            extra="聚焦选择框时自动从 Base URL 获取模型列表"
          >
            <Select
              showSearch
              optionFilterProp="label"
              loading={listModels.isPending}
              onFocus={() => void handleFetchModels()}
              placeholder={models.length > 0 ? '选择模型' : '聚焦后自动获取模型'}
              notFoundContent={
                listModels.isPending ? '正在获取模型…' : '请先填写 Base URL 与 API Key'
              }
              options={modelOptions}
            />
          </Form.Item>
        </Form>
        {error && <Alert type="error" showIcon title={error} />}
      </Modal>
    </Space>
  )
}
