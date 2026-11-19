import { Alert, Button, Empty, Space, Tag, Typography } from 'antd'
import { useState } from 'react'
import { useLocation, useParams } from 'react-router-dom'
import { APIError } from '../../api/client'
import { useSession } from '../../api/queries'
import ChatPanel from '../chat/ChatPanel'
import FilePanel from '../files/FilePanel'

interface SessionLocationState {
  workspaceWarning?: boolean
}

// SessionPage 展示会话头部（标题、工作区路径、模型）与创建工作区时的并发提示，
// 对话、工具时间线与审批交互由 ChatPanel 承载。
export default function SessionPage() {
  const { id } = useParams()
  const location = useLocation()
  const sessionQuery = useSession(id)
  const [filesOpen, setFilesOpen] = useState(false)
  const state = location.state as SessionLocationState | null
  const session = sessionQuery.data

  if (sessionQuery.isError) {
    const error = sessionQuery.error
    if (error instanceof APIError && error.status === 404) {
      return <Empty description="会话不存在" />
    }
    return (
      <Alert
        type="error"
        showIcon
        title={error instanceof APIError ? error.message : '加载会话失败'}
      />
    )
  }
  if (!session) {
    return <Empty description="加载会话中…" />
  }
  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      {state?.workspaceWarning && (
        <Alert
          type="warning"
          showIcon
          title="其他会话正在此目录执行任务，注意并发修改风险"
        />
      )}
      <Typography.Title level={4} style={{ margin: 0 }}>
        {session.title}
      </Typography.Title>
      <Space wrap>
        <Typography.Text code>{session.workspace.path}</Typography.Text>
        <Tag>{session.model.name}</Tag>
        <Tag color="blue">{session.status}</Tag>
        <Button size="small" onClick={() => setFilesOpen((open) => !open)}>
          {filesOpen ? '收起文件' : '文件'}
        </Button>
      </Space>
      <div style={{ display: 'flex', gap: 16, alignItems: 'flex-start' }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <ChatPanel key={session.id} session={session} />
        </div>
        {filesOpen && (
          <div style={{ width: 360, flexShrink: 0 }}>
            <FilePanel workspaceId={session.workspace.id} />
          </div>
        )}
      </div>
    </Space>
  )
}
