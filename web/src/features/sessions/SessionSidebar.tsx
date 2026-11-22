import { Button, Input, List, message, Space, Tag, Typography } from 'antd'
import { useState } from 'react'
import { NavLink, useMatch, useNavigate } from 'react-router-dom'
import { APIError } from '../../api/client'
import {
  useCreateSession,
  useDeleteSession,
  useSessionList,
} from '../../api/queries'
import type { SessionView } from '../../api/types'

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
  const navigate = useNavigate()
  const match = useMatch('/sessions/:id')
  const selectedId = match?.params.id
  const sessionsQuery = useSessionList()
  const createSession = useCreateSession()
  const deleteSession = useDeleteSession()

  const keyword = search.trim()
  const sessions = (sessionsQuery.data ?? [])
    .filter((session) => matchesKeyword(session, keyword))
    .sort((a, b) => b.last_active_at.localeCompare(a.last_active_at))

  async function handleCreate() {
    try {
      const result = await createSession.mutateAsync({ path: '' })
      navigate(`/sessions/${result.id}`)
    } catch (err) {
      message.error(err instanceof APIError ? err.message : '创建会话失败')
    }
  }

  async function handleDelete(id: string) {
    try {
      await deleteSession.mutateAsync(id)
      if (id === selectedId) {
        navigate('/')
      }
    } catch (err) {
      message.error(err instanceof APIError ? err.message : '删除会话失败')
    }
  }

  return (
    <div className="session-sidebar">
      <Typography.Title level={5} style={{ margin: 0 }}>
        Euler 工作台
      </Typography.Title>
      <Button
        type="primary"
        block
        loading={createSession.isPending}
        onClick={() => void handleCreate()}
      >
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
            actions={[
              <Button
                key="delete"
                type="link"
                size="small"
                danger
                onClick={(event) => {
                  event.stopPropagation()
                  void handleDelete(session.id)
                }}
              >
                删除
              </Button>,
            ]}
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
    </div>
  )
}
