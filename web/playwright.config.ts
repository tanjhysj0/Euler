import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineConfig } from '@playwright/test'

// web 包是 ESM（type: module），__dirname 不可用，用 import.meta.url 推导。
const configDir = path.dirname(fileURLToPath(import.meta.url))

// E2E 编排真实 Go 后端 + SQLite + 模拟 OpenAI，不依赖付费模型。
// Go 服务 cwd 固定为仓库根目录，保证 file://migrations 始终可解析。
export default defineConfig({
  testDir: './tests',
  // 真实模型冒烟需要密钥且会调用外部服务，单独由 playwright.real.config.ts 运行。
  testIgnore: /real-model\.spec\.ts/,
  workers: 1,
  timeout: 60_000,
  globalSetup: './tests/global-setup.ts',
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1:18080',
    trace: 'retain-on-failure',
  },
  webServer: [
    {
      command: 'node tests/mock-openai.mjs',
      cwd: configDir,
      url: 'http://127.0.0.1:18081/healthz',
      reuseExistingServer: !process.env.CI,
    },
    {
      // Playwright 先启动 webServer 再执行 globalSetup，因此数据库重置必须
      // 链在 Go 服务启动之前，否则启动时的迁移会被 globalSetup 清掉。
      // SQLite 是单文件库：直接删除旧文件，服务启动时会自动迁移建表。
      // 后端 embed web/dist，先重新构建前端，避免服务陈旧 UI。
      command:
        'npm --prefix web run build && ' +
        'rm -f /tmp/euler-e2e.db && ' +
        'go run ./cmd/euler -config web/tests/euler-e2e.yaml',
      cwd: path.resolve(configDir, '..'),
      url: 'http://127.0.0.1:18080/health',
      timeout: 120_000,
      reuseExistingServer: !process.env.CI,
      env: {
        EULER_DATABASE_URL: '/tmp/euler-e2e.db',
        EULER_TEST_MODEL_KEY: 'euler-not-a-real-secret',
      },
    },
  ],
})
