import { Alert, Button, Empty, Tag, Typography } from 'antd'
import { useState } from 'react'
import { useParams } from 'react-router-dom'
import { APIError } from '../../api/client'
import { useSession } from '../../api/queries'
import ChatPanel from '../chat/ChatPanel'
import FilePanel from '../files/FilePanel'

// SessionPage 展示会话头部（标题、工作区路径、模型），右侧常驻工作区文件树，
// 对话、工具时间线与审批交互由 ChatPanel 承载。
export default function SessionPage() {
  const { id } = useParams()
  const sessionQuery = useSession(id)
  const [filesOpen, setFilesOpen] = useState(true)
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
    <div className="session-page">
      <div className="session-heading">
        <Typography.Title level={4} className="session-title" style={{ margin: 0 }}>
          {session.title}
        </Typography.Title>
        <div className="session-meta">
          <Typography.Text code className="session-path">
            {session.workspace.path}
          </Typography.Text>
          <Tag className="session-model-tag">{session.model.name}</Tag>
          <Tag color="blue">{session.status}</Tag>
          <Button
            size="small"
            type="text"
            className="session-files-toggle"
            onClick={() => setFilesOpen((open) => !open)}
          >
            {filesOpen ? '收起文件' : '展开文件'}
          </Button>
        </div>
      </div>
      <div className="session-body">
        <div className="session-chat">
          <ChatPanel key={session.id} session={session} />
        </div>
        {filesOpen && (
          <div className="session-files">
            <FilePanel workspaceId={session.workspace.id} />
          </div>
        )}
      </div>
    </div>
  )
}
