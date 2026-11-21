import { Layout } from 'antd'
import { Outlet } from 'react-router-dom'
import SessionSidebar from '../features/sessions/SessionSidebar'

// WorkbenchLayout：左侧会话栏 + 中间内容区；工作区文件树由会话页右侧承载。
export default function WorkbenchLayout() {
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
    </Layout>
  )
}
