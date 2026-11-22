import { Layout } from 'antd'
import { Outlet } from 'react-router-dom'
import SessionSidebar from '../features/sessions/SessionSidebar'

// WorkbenchLayout：左侧会话栏 + 中间内容区；工作区文件树由会话页右侧承载。
// 颜色、边框与内边距全部交给主题 CSS 变量，切换主题无需改结构。
export default function WorkbenchLayout() {
  return (
    <Layout className="workbench-layout">
      <Layout.Sider width={280} theme="light" className="workbench-sider">
        <SessionSidebar />
      </Layout.Sider>
      <Layout.Content className="workbench-content">
        <Outlet />
      </Layout.Content>
    </Layout>
  )
}
