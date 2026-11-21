import { expect, test } from '@playwright/test'

// 真实模型冒烟：验证 Web → 后端 → 真实模型 → 工具执行 → 文件树的完整链路。
// 覆盖 mock E2E 无法验证的部分（真实流式响应、真实工具调用）。
const BASE_URL = process.env.EULER_SMOKE_BASE_URL ?? 'https://api.deepseek.com/v1'
const MODEL = process.env.EULER_SMOKE_MODEL ?? 'deepseek-v4-flash'
const PROFILE_NAME = '真实模型冒烟'

test('真实模型完成一轮任务并生成文件', async ({ page }) => {
  test.setTimeout(180_000)
  await page.goto('/settings')
  await page.getByRole('button', { name: '新建模型配置' }).click()
  await page.getByLabel('名称').fill(PROFILE_NAME)
  await page.getByLabel('Base URL').fill(BASE_URL)
  await page.getByLabel('模型', { exact: true }).fill(MODEL)
  await page.getByRole('radio', { name: '环境变量' }).click()
  await page.getByLabel('环境变量名').fill('OPENAI_API_KEY')
  await page.getByRole('button', { name: '保存' }).click()
  await expect(page.getByText(PROFILE_NAME)).toBeVisible()

  await page.goto('/')
  await page.getByRole('button', { name: '新建会话' }).click()
  await page.getByRole('button', { name: '创建会话' }).click()
  await expect(page.getByPlaceholder('输入消息')).toBeVisible()

  await page
    .getByPlaceholder('输入消息')
    .fill('在当前工作区根目录创建 smoke.txt，内容写 hello，完成后告诉我')
  await page.getByPlaceholder('输入消息').press('Enter')

  const done = page.getByText('已完成', { exact: true })
  const failed = page.getByText('失败', { exact: true })
  await Promise.race([
    done.waitFor({ timeout: 150_000 }),
    failed.waitFor({ timeout: 150_000 }),
  ])
  if (await failed.isVisible()) {
    const text = (await page.locator('body').innerText()).slice(-800)
    throw new Error(`真实模型运行失败：${text}`)
  }
  await expect(page.getByRole('treeitem', { name: 'smoke.txt' })).toBeVisible()
})
