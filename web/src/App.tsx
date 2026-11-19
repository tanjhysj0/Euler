import { QueryClientProvider } from '@tanstack/react-query'
import { ConfigProvider, Empty } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import { BrowserRouter, Route, Routes } from 'react-router-dom'
import { createQueryClient } from './api/queryClient'
import WorkbenchLayout from './components/WorkbenchLayout'
import ModelProfiles from './features/settings/ModelProfiles'
import SessionPage from './features/sessions/SessionPage'

const queryClient = createQueryClient()

function HomePage() {
  return <Empty description="选择左侧会话，或新建一个会话开始" />
}

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ConfigProvider locale={zhCN} button={{ autoInsertSpace: false }}>
        <BrowserRouter>
          <Routes>
            <Route element={<WorkbenchLayout />}>
              <Route index element={<HomePage />} />
              <Route path="sessions/:id" element={<SessionPage />} />
              <Route path="settings" element={<ModelProfiles />} />
            </Route>
          </Routes>
        </BrowserRouter>
      </ConfigProvider>
    </QueryClientProvider>
  )
}
