import react from '@vitejs/plugin-react'
import { configDefaults, defineConfig } from 'vitest/config'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false, ws: false },
      '/health': { target: 'http://127.0.0.1:8080', changeOrigin: false, ws: false },
    },
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['src/test/setup.ts'],
    // tests/ 下是 Playwright E2E 用例，不属于 vitest 单测。
    exclude: [...configDefaults.exclude, 'tests/**'],
  },
})
