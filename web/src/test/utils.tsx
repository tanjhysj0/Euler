import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import { ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import type { ReactElement, ReactNode } from 'react'
import { MemoryRouter } from 'react-router-dom'

// renderWithProviders 用与应用一致的 Provider 包裹被测组件；测试中关闭重试。
// 通过 wrapper 选项包裹，rerender 时 Provider 与 QueryClient 保持不变。
export function renderWithProviders(ui: ReactElement, initialEntries?: string[]) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>
        <ConfigProvider locale={zhCN} button={{ autoInsertSpace: false }}>
          <MemoryRouter initialEntries={initialEntries}>{children}</MemoryRouter>
        </ConfigProvider>
      </QueryClientProvider>
    )
  }
  return render(ui, { wrapper: Wrapper })
}
