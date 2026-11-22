import { expect, test } from '@playwright/test'

// 主题 E2E：验证四套主题可切换、根节点属性生效，且刷新后仍保持选择。
test('四套主题可动态切换并在刷新后保持', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'graphite')

  const themes: Array<{ label: string; id: string }> = [
    { label: '纸白', id: 'paper' },
    { label: '青瓷', id: 'celadon' },
    { label: '午夜霓虹', id: 'midnight' },
    { label: '石墨', id: 'graphite' },
  ]

  for (const theme of themes) {
    await page.getByLabel('切换主题').click()
    const menu = page.locator('.ant-dropdown')
    await expect(menu).toBeVisible()
    await menu.getByText(theme.label, { exact: true }).click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme.id)
    await page.reload()
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme.id)
  }
})
