import { QueryClientProvider } from '@tanstack/react-query'
import { ConfigProvider, Empty } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import { useEffect } from 'react'
import { BrowserRouter, Route, Routes } from 'react-router-dom'
import { createQueryClient } from './api/queryClient'
import WorkbenchLayout from './components/WorkbenchLayout'
import ModelProfiles from './features/settings/ModelProfiles'
import SessionPage from './features/sessions/SessionPage'
import { THEMES } from './theme/themes'
import { applyTheme, useThemeStore } from './theme/themeStore'

const queryClient = createQueryClient()

function HomePage() {
  return (
    <div className="home-empty">
      <Empty description="选择左侧会话，或新建一个会话开始" />
    </div>
  )
}

export default function App() {
  const theme = useThemeStore((state) => state.theme)

  // 主题变化时同步根节点属性；首帧已在 main.tsx 应用，这里兜底外部修改。
  useEffect(() => {
    applyTheme(theme)
  }, [theme])

  return (
    <QueryClientProvider client={queryClient}>
      <ConfigProvider
        locale={zhCN}
        theme={THEMES[theme].antd}
        button={{ autoInsertSpace: false }}
      >
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
