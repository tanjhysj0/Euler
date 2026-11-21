import { Button, Input, List, Space, Tag, Typography } from 'antd'
import { useState } from 'react'
import { NavLink, useMatch, useNavigate } from 'react-router-dom'
import {
  useCreateSession,
  useModelProfiles,
  useSessionList,
} from '../../api/queries'
import type { CreateSessionInput, SessionView } from '../../api/types'
import NewSessionDialog from './NewSessionDialog'

function workspaceBasename(path: string): string {
  const parts = path.split('/').filter(Boolean)
  return parts[parts.length - 1] ?? path
}

function matchesKeyword(session: SessionView, keyword: string): boolean {
  if (!keyword) {
    return true
  }
  const lower = keyword.toLowerCase()
  return (
    session.title.toLowerCase().includes(lower) ||
    session.workspace.path.toLowerCase().includes(lower)
  )
}

// SessionSidebar：标题/路径搜索、最近活跃排序、状态位与工作区 basename。
// 后端列表暂无 Run 状态字段，T11 先展示会话 status，Run 状态在 T12 接入。
export default function SessionSidebar() {
  const [search, setSearch] = useState('')
  const [dialogOpen, setDialogOpen] = useState(false)
  const navigate = useNavigate()
  const match = useMatch('/sessions/:id')
  const selectedId = match?.params.id
  const sessionsQuery = useSessionList()
  const profilesQuery = useModelProfiles()
  const createSession = useCreateSession()

  const keyword = search.trim()
  const sessions = (sessionsQuery.data ?? [])
    .filter((session) => matchesKeyword(session, keyword))
    .sort((a, b) => b.last_active_at.localeCompare(a.last_active_at))

  async function handleCreate(input: CreateSessionInput) {
    const result = await createSession.mutateAsync(input)
    setDialogOpen(false)
    navigate(`/sessions/${result.id}`)
    return result
  }

  return (
    <div className="session-sidebar">
      <Typography.Title level={5} style={{ margin: 0 }}>
        Euler 工作台
      </Typography.Title>
      <Button type="primary" block onClick={() => setDialogOpen(true)}>
        新建会话
      </Button>
      <Input
        placeholder="搜索标题或路径"
        allowClear
        value={search}
        onChange={(event) => setSearch(event.target.value)}
      />
      <List
        className="session-sidebar-list"
        dataSource={sessions}
        locale={{ emptyText: '暂无会话' }}
        renderItem={(session) => (
          <List.Item
            className={`session-sidebar-item${session.id === selectedId ? ' active' : ''}`}
            onClick={() => navigate(`/sessions/${session.id}`)}
          >
            <List.Item.Meta
              title={session.title}
              description={
                <Space size={4} wrap>
                  <span className="session-sidebar-path">
                    {workspaceBasename(session.workspace.path)}
                  </span>
                  <Tag color={session.status === 'active' ? 'blue' : 'default'}>
                    {session.status}
                  </Tag>
                </Space>
              }
            />
          </List.Item>
        )}
      />
      <NavLink to="/settings">模型设置</NavLink>
      <NewSessionDialog
        open={dialogOpen}
        profiles={profilesQuery.data ?? []}
        onCreate={handleCreate}
        onCancel={() => setDialogOpen(false)}
      />
    </div>
  )
}
