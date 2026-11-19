import { Button, Layout, Typography } from 'antd'
import { useState } from 'react'
import { Outlet } from 'react-router-dom'
import SessionSidebar from '../features/sessions/SessionSidebar'

// WorkbenchLayout：左侧会话栏、中间 Outlet、右侧可折叠文件区占位
// （文件树与预览在后续任务中实现）。
export default function WorkbenchLayout() {
  const [filesCollapsed, setFilesCollapsed] = useState(false)
  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Layout.Sider
        width={280}
        theme="light"
        style={{ borderInlineEnd: '1px solid #f0f0f0', padding: 12 }}
      >
        <SessionSidebar />
      </Layout.Sider>
      <Layout.Content style={{ padding: 16 }}>
        <Outlet />
      </Layout.Content>
      <Layout.Sider
        width={300}
        collapsedWidth={48}
        theme="light"
        collapsible
        trigger={null}
        collapsed={filesCollapsed}
        style={{ borderInlineStart: '1px solid #f0f0f0', padding: 12 }}
      >
        <Button block onClick={() => setFilesCollapsed((value) => !value)}>
          {filesCollapsed ? '展开' : '收起文件区'}
        </Button>
        {!filesCollapsed && (
          <Typography.Paragraph type="secondary" style={{ marginTop: 12 }}>
            工作区文件区将在后续任务中提供。
          </Typography.Paragraph>
        )}
      </Layout.Sider>
    </Layout>
  )
}
