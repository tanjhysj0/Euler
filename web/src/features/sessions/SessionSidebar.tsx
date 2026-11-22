import { Button, Input, List, message, Space, Tag } from 'antd'
import { useState } from 'react'
import { NavLink, useMatch, useNavigate } from 'react-router-dom'
import { APIError } from '../../api/client'
import {
  useCreateSession,
  useDeleteSession,
  useSessionList,
} from '../../api/queries'
import type { SessionView } from '../../api/types'
import ThemeSwitcher from '../../components/ThemeSwitcher'

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

// SessionSidebar：品牌区、标题/路径搜索、最近会话与主题切换入口。
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
      <div className="sidebar-brand">
        <span className="sidebar-logo">E</span>
        <div className="sidebar-brand-text">
          <strong>Euler</strong>
          <small>编码 Agent 工作台</small>
        </div>
      </div>
      <Button
        type="primary"
        block
        className="new-session-button"
        loading={createSession.isPending}
        onClick={() => void handleCreate()}
      >
        新建会话
      </Button>
      <Input
        className="session-search"
        placeholder="搜索标题或路径"
        allowClear
        value={search}
        onChange={(event) => setSearch(event.target.value)}
      />
      <div className="sidebar-section-label">最近</div>
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
                className="session-delete"
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
      <div className="sidebar-footer">
        <ThemeSwitcher />
        <NavLink className="sidebar-link" to="/settings">
          模型设置
        </NavLink>
      </div>
    </div>
  )
}
