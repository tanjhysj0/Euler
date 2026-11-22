import { expect, test, type Page } from '@playwright/test'

const PROFILE_NAME = '本地模拟模型'
const ALT_PROFILE_NAME = '备用模拟模型'

// ensureProfile 幂等创建 E2E 模型配置：指向本地 mock，密钥走环境变量引用。
async function ensureProfile(page: Page, name = PROFILE_NAME) {
  await page.goto('/settings')
  if (await page.getByText(name).count()) {
    return
  }
  await page.getByRole('button', { name: '新建模型配置' }).click()
  await page.getByLabel('名称').fill(name)
  await page.getByLabel('Base URL').fill('http://127.0.0.1:18081/v1')
  await page.getByRole('radio', { name: '环境变量' }).click()
  await page.getByLabel('环境变量名').fill('EULER_TEST_MODEL_KEY')
  await fetchAndSelectModel(page, 'euler-e2e')
  await page.getByRole('button', { name: '保存' }).click()
  await expect(page.getByText(name)).toBeVisible()
}

// fetchAndSelectModel 聚焦模型选择框触发远程拉取，mock 只返回一个模型，
// 页面会自动选中它。
async function fetchAndSelectModel(page: Page, model: string) {
  await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().includes('/model-profiles/models') && response.ok(),
    ),
    page.locator('#model').click(),
  ])
  await expect(
    page.getByRole('dialog').getByText(model, { exact: true }).first(),
  ).toBeVisible()
}

// newSession 新建一个使用默认自动工作区的会话，并等待输入框就绪。
async function newSession(page: Page) {
  await page.goto('/')
  await page.getByRole('button', { name: '新建会话' }).click()
  await expect(page.getByPlaceholder('输入消息')).toBeVisible()
}

async function send(page: Page, text: string) {
  await page.getByPlaceholder('输入消息').fill(text)
  await page.getByRole('button', { name: '发送' }).click()
}

test('自动工作区、工具执行与产物预览', async ({ page }) => {
  await ensureProfile(page)
  await newSession(page)
  await send(page, '创建验收文档')

  // 工具执行完成后时间线展示自然语言动作、成功状态与结果，Run 进入已完成。
  await expect(page.locator('.tool-row[data-tool="file_write"]').first()).toContainText('写入文件')
  await expect(page.getByText('成功').first()).toBeVisible()
  await expect(page.getByText('已完成')).toBeVisible()
  await expect(page.getByText(/可以在右侧预览/)).toBeVisible()

  // 右侧文件区默认常驻，直接展示工作区文件树。
  await expect(page.getByRole('treeitem', { name: 'note.txt' })).toBeVisible()
  await page.getByRole('treeitem', { name: 'note.txt' }).click()
  await expect(page.locator('pre')).toContainText('# Euler 验收')

  // 刷新后历史消息、工具时间线与产物依旧可恢复。
  await page.reload()
  await expect(page.locator('.tool-row[data-tool="file_write"]').first()).toContainText('写入文件')
  await expect(page.getByRole('treeitem', { name: 'note.txt' })).toBeVisible()
  await page.getByRole('treeitem', { name: 'note.txt' }).click()
  await expect(page.locator('pre')).toContainText('# Euler 验收')
})

test('continue 后刷新，两次 Run 的工具时间线都保留', async ({ page }) => {
  await ensureProfile(page)
  await newSession(page)
  await send(page, '创建验收文档')
  await expect(page.locator('.tool-timeline').first()).toContainText('note.txt')
  await expect(page.getByText('已完成')).toBeVisible()

  // 同一会话继续第二次 Run，上一次的时间线不能被覆盖。
  await send(page, '创建多个文件')
  await expect(page.locator('.tool-timeline').last()).toContainText('b.txt')
  await expect(page.getByText('已完成')).toBeVisible()

  // 刷新后从数据库重建：两条时间线都在，且各自保留自己的文件。
  await page.reload()
  await expect(page.locator('.tool-timeline')).toHaveCount(2)
  await expect(page.locator('.tool-timeline').first()).toContainText('note.txt')
  await expect(page.locator('.tool-timeline').last()).toContainText('a.txt')
  await expect(page.locator('.tool-timeline').last()).toContainText('b.txt')
})

test('慢速流刷新无重复，停止后已取消且刷新保持一致', async ({ page }) => {
  await ensureProfile(page)
  await newSession(page)
  await send(page, '持续输出')
  await expect(page.locator('body')).toContainText('片段[0001]')
  await expect(page.locator('body')).toContainText('片段[0005]')

  // slow 流期间刷新：恢复的文本来自权威 content + 事件回放，不得重复。
  await page.reload()
  await expect(page.locator('body')).toContainText('片段[0005]')
  const text = await page.locator('body').innerText()
  const chunks = text.match(/片段\[\d{4}\]/g) ?? []
  expect(chunks.length).toBeGreaterThan(0)
  expect(new Set(chunks).size).toBe(chunks.length)

  await page.getByRole('button', { name: '停止' }).click()
  await page.getByRole('button', { name: '确认停止' }).click()
  await expect(page.getByText('已取消')).toBeVisible()
  const cancelledText = await page.getByText('助手', { exact: true }).locator('..').innerText()

  // 刷新后 Run 状态与助手消息文本必须与停止时一致。
  // 两侧都用 innerText 比较：toHaveText 基于 textContent，不含布局换行。
  await page.reload()
  await expect(page.getByText('已取消')).toBeVisible()
  await expect
    .poll(() => page.getByText('助手', { exact: true }).locator('..').innerText())
    .toBe(cancelledText)
})

test('取消后可以立即发起新的 Run', async ({ page }) => {
  await ensureProfile(page)
  await newSession(page)
  await send(page, '持续输出')
  await expect(page.locator('body')).toContainText('片段[0001]')

  await page.getByRole('button', { name: '停止' }).click()
  await page.getByRole('button', { name: '确认停止' }).click()
  await expect(page.getByText('已取消')).toBeVisible()

  // 取消后输入区恢复可用，新 Run 不受上一条终态 Run 影响。
  await send(page, '快速完成')
  await expect(page.getByText('已完成')).toBeVisible()
  await expect(page.getByText('快速完成。', { exact: true })).toBeVisible()
})

test('模型 5xx 错误展示失败状态与错误信息', async ({ page }) => {
  await ensureProfile(page)
  await newSession(page)
  await send(page, '触发模型错误')

  await expect(page.getByText('失败').first()).toBeVisible()
  const alert = page.getByRole('alert')
  await expect(alert).toBeVisible()
  await expect(alert).toContainText('500')
})

test('工具执行失败在时间线展示错误结果', async ({ page }) => {
  await ensureProfile(page)
  await newSession(page)
  await send(page, '读取缺失文件')

  const failedTool = page.locator('.tool-row[data-tool="file_read"]').first()
  await expect(failedTool).toContainText('读取文件')
  await expect(page.getByText('失败').first()).toBeVisible()
  await expect(failedTool).toContainText(/文件不存在|file_not_found/)
  // 工具失败是模型可观察到的结果，Run 本身仍能正常收尾。
  await expect(page.getByText('读取失败，任务结束。')).toBeVisible()
  await expect(page.getByText('已完成')).toBeVisible()
})

test('单轮写入多个文件后文件树可分别预览', async ({ page }) => {
  await ensureProfile(page)
  await newSession(page)
  await send(page, '创建多个文件')

  await expect(page.locator('.tool-row[data-tool="file_write"]')).toHaveCount(2)
  await expect(page.getByText('已创建两个文件。')).toBeVisible()
  await expect(page.getByText('已完成')).toBeVisible()

  await expect(page.getByRole('treeitem', { name: 'a.txt' })).toBeVisible()
  await page.getByRole('treeitem', { name: 'a.txt' }).click()
  await expect(page.locator('pre')).toContainText('alpha-content')
  await page.getByRole('treeitem', { name: 'b.txt' }).click()
  await expect(page.locator('pre')).toContainText('beta-content')
})

test('右侧文件树支持目录点击展开与收起', async ({ page }) => {
  await ensureProfile(page)
  await newSession(page)
  await send(page, '创建嵌套文件')

  await expect(page.getByText('已完成')).toBeVisible()
  const directory = page.getByRole('treeitem', { name: 'src' })
  await expect(directory).toBeVisible()
  await expect(page.getByRole('treeitem', { name: 'main.go' })).toBeVisible()

  // 点击目录收起子项，再次点击恢复。
  await directory.click()
  await expect(page.getByRole('treeitem', { name: 'main.go' })).toHaveCount(0)
  await directory.click()
  const file = page.getByRole('treeitem', { name: 'main.go' })
  await expect(file).toBeVisible()

  await file.click()
  await expect(page.locator('pre')).toContainText('package main')
})

test('思考过程以折叠区展示并保留到 Run 完成', async ({ page }) => {
  await ensureProfile(page)
  await newSession(page)
  await send(page, '展示思考')

  // Run 完成后正文可见，思考过程折叠区仍保留，展开可读。
  await expect(page.getByText('思考完成，结论如下。')).toBeVisible()
  await expect(page.getByText('已完成')).toBeVisible()
  const reasoning = page.locator('.reasoning-block')
  await expect(reasoning).toBeVisible()
  await expect(reasoning.locator('summary')).toHaveText('思考过程')
  await reasoning.locator('summary').click()
  await expect(reasoning.locator('.reasoning-text')).toContainText('先分析任务，再给出结论。')
})

test('历史中的空助手消息不会导致下一轮 400', async ({ page }) => {
  await ensureProfile(page)
  await newSession(page)
  // 第一轮只调用 submit、没有正文，产生一条 content 为空的助手消息。
  await send(page, '提交空总结')
  await expect(page.getByText('已完成')).toBeVisible()

  // 第二轮会把历史发给模型；mock 按 DeepSeek 规则拒绝空助手消息，
  // 加载历史时必须过滤掉它，否则这里会失败。
  await send(page, '快速完成')
  await expect(page.getByText('快速完成。', { exact: true })).toBeVisible()
  await expect(page.getByText('失败')).toHaveCount(0)
})

test('新建会话使用默认模型，并可在输入区切换模型', async ({ page }) => {
  await ensureProfile(page)
  await ensureProfile(page, ALT_PROFILE_NAME)
  await newSession(page)
  // 新建会话不再选择模型，输入区默认展示最早创建的模型配置。
  await expect(page.getByText(PROFILE_NAME).first()).toBeVisible()

  // 通过输入区右下角的模型下拉切换，会话头部同步展示新模型。
  await page.locator('.composer').getByRole('combobox').click()
  const dropdown = page.locator('.ant-select-dropdown:not(.ant-select-dropdown-hidden)')
  await dropdown.getByText(ALT_PROFILE_NAME, { exact: true }).click()
  await expect(page.getByText(ALT_PROFILE_NAME).first()).toBeVisible()
})

test('连续新建的会话自动分配不同工作区', async ({ page }) => {
  await ensureProfile(page)
  await newSession(page)
  const firstPath = await page
    .locator('code', { hasText: '/euler-e2e-workspaces/' })
    .innerText()
  await newSession(page)
  const secondPath = await page
    .locator('code', { hasText: '/euler-e2e-workspaces/' })
    .innerText()
  expect(secondPath).not.toBe(firstPath)
})

test('多会话创建、搜索过滤与切换', async ({ page }) => {
  await ensureProfile(page)
  await page.goto('/')
  const items = page.locator('.session-sidebar-item')
  await expect(page.locator('.session-sidebar-list')).toBeVisible()
  const before = await items.count()

  await newSession(page)
  await page.getByRole('button', { name: '新建会话' }).click()
  await expect(items).toHaveCount(before + 2)

  // 搜索只保留匹配标题的会话。
  const title = (await items.first().innerText()).split('\n')[0].trim()
  await page.getByPlaceholder('搜索标题或路径').fill(title)
  await expect(items).toHaveCount(1)
  await page.getByPlaceholder('搜索标题或路径').fill('')
  await expect(items).toHaveCount(before + 2)

  // 点击侧栏项切换会话，右侧对话面板随之加载。
  await items.first().click()
  await expect(page.getByPlaceholder('输入消息')).toBeVisible()
})

test('模型配置支持编辑与删除', async ({ page }) => {
  const name = '待删除模型'
  const editedName = '已改名模型'
  // 后端要求至少保留一个模型配置，先确保默认配置存在。
  await ensureProfile(page)
  await page.goto('/settings')
  await page.getByRole('button', { name: '新建模型配置' }).click()
  await page.getByLabel('名称').fill(name)
  await page.getByLabel('Base URL').fill('http://127.0.0.1:18081/v1')
  await page.getByRole('radio', { name: '环境变量' }).click()
  await page.getByLabel('环境变量名').fill('EULER_TEST_MODEL_KEY')
  await fetchAndSelectModel(page, 'euler-e2e')
  await page.getByRole('button', { name: '保存' }).click()

  const row = page.getByRole('row').filter({ hasText: name })
  await expect(row).toBeVisible()

  // 编辑名称后保存，列表展示新名称。
  await row.getByRole('button', { name: '编辑' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('名称').fill(editedName)
  await dialog.getByRole('button', { name: '保存' }).click()
  const editedRow = page.getByRole('row').filter({ hasText: editedName })
  await expect(editedRow).toBeVisible()

  // 删除后该配置从列表消失。
  await editedRow.getByRole('button', { name: '删除' }).click()
  await expect(editedRow).toHaveCount(0)
})
