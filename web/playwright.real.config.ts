import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineConfig } from '@playwright/test'

// 真实模型冒烟配置：不启动 mock OpenAI，后端直接调用真实服务。
// 与默认 E2E 使用同一测试库和工作区配置，避免两套环境再次分叉。
const configDir = path.dirname(fileURLToPath(import.meta.url))

export default defineConfig({
  testDir: './tests',
  testMatch: /real-model\.spec\.ts/,
  workers: 1,
  timeout: 180_000,
  globalSetup: './tests/global-setup.ts',
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1:18080',
    trace: 'retain-on-failure',
  },
  webServer: [
    {
      // 每次重建前端并删除 SQLite 测试库文件，确保真实模型跑在与 mock E2E 相同的环境上。
      command:
        'npm --prefix web run build && ' +
        'rm -f /tmp/euler-e2e.db && ' +
        'go run ./cmd/euler -config web/tests/euler-e2e.yaml',
      cwd: path.resolve(configDir, '..'),
      url: 'http://127.0.0.1:18080/health',
      timeout: 120_000,
      reuseExistingServer: false,
      env: {
        EULER_DATABASE_URL: '/tmp/euler-e2e.db',
        OPENAI_API_KEY: process.env.OPENAI_API_KEY ?? '',
      },
    },
  ],
})
