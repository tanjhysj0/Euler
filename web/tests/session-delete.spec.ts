import { expect, test } from '@playwright/test'

const themes = [
  { id: 'graphite', label: '石墨' },
  { id: 'paper', label: '纸白' },
  { id: 'celadon', label: '青瓷' },
  { id: 'midnight', label: '午夜霓虹' },
]

// ensureProfile 确保 E2E 后端有默认模型配置，否则新建会话会被拒绝。
async function ensureProfile(page: import('@playwright/test').Page) {
  const list = await page.request.get('/api/v1/model-profiles')
  const profiles = (await list.json()) as unknown[]
  if (profiles.length > 0) {
    return
  }
  const created = await page.request.post('/api/v1/model-profiles', {
    data: {
      name: '本地模拟模型',
      base_url: 'http://127.0.0.1:18081/v1',
      model: 'euler-e2e',
      secret_kind: 'environment',
      secret_ref: 'EULER_TEST_MODEL_KEY',
    },
  })
  expect(created.ok()).toBeTruthy()
}

// 回归：四套主题下会话删除入口都可见、可点击，且删除后能从侧栏移除。
test('四套主题下都能删除会话', async ({ page }) => {
  await page.goto('/')
  await ensureProfile(page)
  await page.reload()
  for (const theme of themes) {
    await page.getByRole('button', { name: '切换主题' }).click()
    await page.getByRole('menuitem', { name: new RegExp(theme.label) }).click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme.id)

    await page.getByRole('button', { name: '新建会话' }).click()
    await expect(page).toHaveURL(/\/sessions\/[0-9a-f-]+$/)
    const active = page.locator('.session-sidebar-item.active')
    await expect(active).toHaveCount(1)
    await active.hover()
    const del = active.getByRole('button', { name: '删除' })
    await expect(del).toBeVisible()
    await active.screenshot({
      path: `.screenshots/session-delete-${theme.id}.png`,
    })

    await del.click()
    await expect(page).toHaveURL(/\/$/)
    await expect(active).toHaveCount(0)
  }
})
