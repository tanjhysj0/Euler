import { QueryClient } from '@tanstack/react-query'

// GET 查询失败重试一次；写请求（mutation）不重试，避免重复副作用。
export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: 1 },
      mutations: { retry: 0 },
    },
  })
}
