import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test, type Page } from '@playwright/test'

// UI 巡检：按真实用户路径逐步操作 Web 控制台，并在每一步操作后截图留证。
// 运行环境与默认 E2E 一致（18080 后端 + 18081 模拟模型 + euler_test 库），
// 不会触碰本地调试库与 ~/EulerWorkspaces。
const SHOT_DIR = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  '../../.screenshots/webui-tour',
)
const PROFILE_NAME = '本地模拟模型'
const ALT_PROFILE_NAME = '备用模拟模型'
const MOCK_BASE_URL = 'http://127.0.0.1:18081/v1'

test.use({ viewport: { width: 1440, height: 900 } })

// shot 等待布局与动画稳定后截整页，文件名按操作顺序编号。
async function shot(page: Page, name: string) {
  await page.waitForTimeout(250)
  await page.screenshot({ path: path.join(SHOT_DIR, name), fullPage: true })
}

// fillProfileDialog 填写新建模型配置表单，并触发远程模型列表拉取。
async function fillProfileDialog(page: Page, name: string) {
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  await dialog.getByLabel('名称').fill(name)
  await dialog.getByLabel('Base URL').fill(MOCK_BASE_URL)
  await dialog.getByRole('radio', { name: '环境变量' }).click()
  await dialog.getByLabel('环境变量名').fill('EULER_TEST_MODEL_KEY')
  await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().includes('/model-profiles/models') && response.ok(),
    ),
    dialog.locator('#model').click(),
  ])
  await expect(dialog.getByText('euler-e2e', { exact: true }).first()).toBeVisible()
}

// sendMessage 在输入区填写消息并点击发送。
async function sendMessage(page: Page, text: string) {
  await page.getByPlaceholder('输入消息').fill(text)
  await page.getByRole('button', { name: '发送' }).click()
}

test('Web 控制台全流程操作截图巡检', async ({ page }) => {
  test.setTimeout(240_000)
  const consoleErrors: string[] = []
  const badResponses: string[] = []
  page.on('console', (message) => {
    if (message.type() === 'error') {
      consoleErrors.push(message.text())
    }
  })
  page.on('response', (response) => {
    if (response.status() >= 400) {
      badResponses.push(`${response.status()} ${response.url()}`)
    }
  })

  fs.rmSync(SHOT_DIR, { recursive: true, force: true })
  fs.mkdirSync(SHOT_DIR, { recursive: true })

  // 1. 首页空态
  await page.goto('/')
  await expect(page.getByText('选择左侧会话，或新建一个会话开始')).toBeVisible()
  await shot(page, '01-home-empty.png')

  // 2. 进入模型设置页
  await page.getByRole('link', { name: '模型设置' }).click()
  await expect(page.getByRole('heading', { name: '模型设置' })).toBeVisible()
  await shot(page, '02-settings-empty.png')

  // 3. 打开新建模型配置对话框
  await page.getByRole('button', { name: '新建模型配置' }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
  await shot(page, '03-profile-dialog-empty.png')

  // 4. 填写模型配置（环境变量密钥方式）
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('名称').fill(PROFILE_NAME)
  await dialog.getByLabel('Base URL').fill(MOCK_BASE_URL)
  await dialog.getByRole('radio', { name: '环境变量' }).click()
  await dialog.getByLabel('环境变量名').fill('EULER_TEST_MODEL_KEY')
  await shot(page, '04-profile-form-filled.png')

  // 5. 聚焦模型下拉框，自动拉取远程模型列表
  await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().includes('/model-profiles/models') && response.ok(),
    ),
    dialog.locator('#model').click(),
  ])
  await expect(dialog.getByText('euler-e2e', { exact: true }).first()).toBeVisible()
  await shot(page, '05-profile-model-fetched.png')

  // 6. 保存模型配置
  await dialog.getByRole('button', { name: '保存' }).click()
  await expect(page.getByText(PROFILE_NAME)).toBeVisible()
  await shot(page, '06-profile-saved.png')

  // 7. 新建会话（自动分配工作区）
  await page.goto('/')
  await page.getByRole('button', { name: '新建会话' }).click()
  await expect(page.getByPlaceholder('输入消息')).toBeVisible()
  await expect(page.getByText('暂无文件，运行任务后产物会出现在这里')).toBeVisible()
  await shot(page, '07-session-created.png')

  // 8. 输入消息
  await page.getByPlaceholder('输入消息').fill('持续输出')
  await shot(page, '08-message-typed.png')

  // 9. 发送后观察流式输出与运行中状态
  await page.getByRole('button', { name: '发送' }).click()
  await expect(page.locator('body')).toContainText('片段[0001]')
  await shot(page, '09-run-streaming.png')

  // 10. 停止任务确认弹窗
  await page.getByRole('button', { name: '停止' }).click()
  await expect(page.getByText('停止当前任务')).toBeVisible()
  await shot(page, '10-stop-confirm.png')

  // 11. 确认停止，Run 进入已取消
  await page.getByRole('button', { name: '确认停止' }).click()
  await expect(page.getByText('已取消')).toBeVisible()
  await shot(page, '11-run-cancelled.png')

  // 12. 再次发送任务，工具执行完成并生成文件
  await sendMessage(page, '创建验收文档')
  await expect(page.getByText('已完成')).toBeVisible()
  await expect(page.getByText('file_write').first()).toBeVisible()
  await expect(page.getByRole('treeitem', { name: 'note.txt' })).toBeVisible()
  await shot(page, '12-run-succeeded.png')

  // 13. 点击文件树中的产物进行预览
  await page.getByRole('treeitem', { name: 'note.txt' }).click()
  await expect(page.locator('pre')).toContainText('# Euler 验收')
  await shot(page, '13-file-preview.png')

  // 14. 刷新页面，验证历史消息、工具时间线与预览均恢复
  await page.reload()
  await expect(page.getByText('file_write').first()).toBeVisible()
  await page.getByRole('treeitem', { name: 'note.txt' }).click()
  await expect(page.locator('pre')).toContainText('# Euler 验收')
  await shot(page, '14-reload-restore.png')

  // 15. 思考过程折叠展示
  await sendMessage(page, '展示思考')
  await expect(page.getByText('思考完成，结论如下。')).toBeVisible()
  await expect(page.getByText('已完成')).toBeVisible()
  await shot(page, '15-reasoning-collapsed.png')

  // 16. 展开思考过程
  const reasoning = page.locator('.reasoning-block')
  await reasoning.locator('summary').click()
  await expect(reasoning.locator('.reasoning-text')).toContainText('先分析任务，再给出结论。')
  await shot(page, '16-reasoning-expanded.png')

  // 17. 工具执行失败仍能正常收尾
  await sendMessage(page, '读取缺失文件')
  await expect(page.getByText('读取失败，任务结束。')).toBeVisible()
  await expect(page.getByText('file_not_found').first()).toBeVisible()
  await shot(page, '17-tool-failed.png')

  // 18. 单轮创建多个文件，文件树分别展示
  await sendMessage(page, '创建多个文件')
  await expect(page.getByText('已创建两个文件。')).toBeVisible()
  await expect(page.getByText('file_write')).toHaveCount(2)
  await shot(page, '18-multiple-files.png')

  // 19. 预览其中一个文件
  await page.getByRole('treeitem', { name: 'a.txt' }).click()
  await expect(page.locator('pre')).toContainText('alpha-content')
  await shot(page, '19-multi-file-preview.png')

  // 20. 创建嵌套目录文件
  await sendMessage(page, '创建嵌套文件')
  // Markdown 会把 main.go 渲染为行内代码，按整句文本匹配会落空，这里匹配正文片段。
  await expect(page.getByText(/已创建/).first()).toBeVisible()
  await expect(page.getByText('已完成')).toBeVisible()
  await expect(page.getByRole('treeitem', { name: 'main.go' })).toBeVisible()
  await shot(page, '20-nested-tree.png')

  // 21. 点击目录收起子节点
  await page.getByRole('treeitem', { name: 'src' }).click()
  await expect(page.getByRole('treeitem', { name: 'main.go' })).toHaveCount(0)
  await shot(page, '21-directory-collapsed.png')

  // 22. 再次展开目录并预览代码文件
  await page.getByRole('treeitem', { name: 'src' }).click()
  await page.getByRole('treeitem', { name: 'main.go' }).click()
  await expect(page.locator('pre')).toContainText('package main')
  await shot(page, '22-code-preview.png')

  // 23. 模型 5xx 错误展示失败状态与错误信息
  await sendMessage(page, '触发模型错误')
  await expect(page.getByText('失败').first()).toBeVisible()
  await expect(page.getByRole('alert')).toContainText('500')
  await shot(page, '23-model-error.png')

  // 24. 新建第二个模型配置，用于验证会话内切换模型
  await page.goto('/settings')
  await page.getByRole('button', { name: '新建模型配置' }).click()
  await fillProfileDialog(page, ALT_PROFILE_NAME)
  await shot(page, '24-alt-profile-form.png')
  await page.getByRole('dialog').getByRole('button', { name: '保存' }).click()
  await expect(page.getByText(ALT_PROFILE_NAME)).toBeVisible()
  await shot(page, '25-alt-profile-saved.png')

  // 25. 回到会话，在输入区切换模型
  await page.goto('/')
  await page.locator('.session-sidebar-item').first().click()
  await expect(page.getByPlaceholder('输入消息')).toBeVisible()
  await page.locator('.composer').getByRole('combobox').click()
  const dropdown = page.locator('.ant-select-dropdown:not(.ant-select-dropdown-hidden)')
  await dropdown.getByText(ALT_PROFILE_NAME, { exact: true }).click()
  await expect(page.getByText(ALT_PROFILE_NAME).first()).toBeVisible()
  await shot(page, '26-model-switched.png')

  // 26. 会话列表搜索过滤
  await page.goto('/')
  const items = page.locator('.session-sidebar-item')
  const title = (await items.first().innerText()).split('\n')[0].trim()
  await page.getByPlaceholder('搜索标题或路径').fill(title)
  await expect(items).toHaveCount(1)
  await shot(page, '27-session-search.png')
  await page.getByPlaceholder('搜索标题或路径').fill('')
  await expect(items.first()).toBeVisible()

  // 27. 新建会话并切换
  await page.getByRole('button', { name: '新建会话' }).click()
  await expect(page.getByPlaceholder('输入消息')).toBeVisible()
  await shot(page, '28-new-session.png')

  // 28. 删除会话
  const beforeDelete = await items.count()
  await items.first().getByRole('button', { name: '删除' }).click()
  await expect(items).toHaveCount(beforeDelete - 1)
  await shot(page, '29-session-deleted.png')

  // 29. 编辑模型配置
  await page.goto('/settings')
  const altRow = page.getByRole('row').filter({ hasText: ALT_PROFILE_NAME })
  await altRow.getByRole('button', { name: '编辑' }).click()
  await expect(page.getByText('编辑模型配置')).toBeVisible()
  await shot(page, '30-profile-edit-dialog.png')

  // 30. 修改名称并保存
  const editedName = `${ALT_PROFILE_NAME}（已修改）`
  const editDialog = page.getByRole('dialog')
  await editDialog.getByLabel('名称').fill(editedName)
  await editDialog.getByRole('button', { name: '保存' }).click()
  await expect(page.getByText(editedName)).toBeVisible()
  await shot(page, '31-profile-edited.png')

  // 31. 删除模型配置
  const editedRow = page.getByRole('row').filter({ hasText: editedName })
  await editedRow.getByRole('button', { name: '删除' }).click()
  await expect(editedRow).toHaveCount(0)
  await shot(page, '32-profile-deleted.png')

  // 32. 收起右侧文件面板
  await page.goto('/')
  await page.locator('.session-sidebar-item').first().click()
  await page.getByRole('button', { name: '收起文件' }).click()
  await expect(page.getByRole('button', { name: '展开文件' })).toBeVisible()
  await shot(page, '33-files-collapsed.png')

  // 33. 重新展开文件面板
  await page.getByRole('button', { name: '展开文件' }).click()
  await expect(page.getByRole('button', { name: '收起文件' })).toBeVisible()
  await shot(page, '34-files-expanded.png')

  // 34. 执行一次带工具调用的任务，确保工具时间线存在
  await sendMessage(page, '创建验收文档')
  await expect(page.getByText('已完成')).toBeVisible()
  await expect(page.getByText('file_write').first()).toBeVisible()
  await shot(page, '35-timeline-visible.png')

  // 35. 收起工具时间线
  await page.getByRole('button', { name: '收起工具时间线' }).click()
  await expect(page.getByRole('button', { name: '展开工具时间线' })).toBeVisible()
  await shot(page, '36-timeline-collapsed.png')

  // 36. 展开工具时间线
  await page.getByRole('button', { name: '展开工具时间线' }).click()
  await expect(page.getByRole('button', { name: '收起工具时间线' })).toBeVisible()
  await shot(page, '37-timeline-expanded.png')

  // 记录浏览器控制台与网络错误，便于人工复核（不因此判定用例失败）。
  console.log(`控制台错误数量：${consoleErrors.length}`)
  for (const error of consoleErrors.slice(0, 10)) {
    console.log(`- ${error}`)
  }
  console.log(`非成功响应数量：${badResponses.length}`)
  for (const response of badResponses.slice(0, 10)) {
    console.log(`- ${response}`)
  }
})
