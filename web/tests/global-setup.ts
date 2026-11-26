import fs from 'node:fs'

// 每次运行前只清理 E2E 专用工作区根目录，绝不触碰 ~/EulerWorkspaces。
// SQLite 测试库文件（/tmp/euler-e2e.db）的删除在 Go webServer 启动命令中链式执行
// （Playwright 先启动 webServer 再跑 globalSetup，放在这里会清掉已迁移的库）。
export default function globalSetup(): void {
  fs.rmSync('/tmp/euler-e2e-workspaces', { recursive: true, force: true })
}
