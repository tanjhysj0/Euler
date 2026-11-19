import { expect, test } from '@playwright/test'

test('自动工作区、流式对话、文件审批与产物预览', async ({ page }) => {
    await page.goto('/settings')
    await page.getByRole('button', { name: '新建模型配置' }).click()
    await page.getByLabel('名称').fill('本地模拟模型')
    await page.getByLabel('Base URL').fill('http://127.0.0.1:18081/v1')
    await page.getByLabel('模型', { exact: true }).fill('euler-e2e')
    await page.getByRole('radio', { name: '环境变量' }).click()
    await page.getByLabel('环境变量名').fill('EULER_TEST_MODEL_KEY')
    await page.getByRole('button', { name: '保存' }).click()

    await page.goto('/')
    await page.getByRole('button', { name: '新建会话' }).click()
    await expect(page.getByText(/将自动创建默认工作区/)).toBeVisible()
    await page.getByRole('button', { name: '创建会话' }).click()
    await page.getByPlaceholder('输入消息').fill('创建验收文档')
    await page.getByRole('button', { name: '发送' }).click()

    await expect(page.getByText('file_write').first()).toBeVisible()
    await expect(page.getByText('note.txt')).toBeVisible()
    await page.reload()
    await expect(page.getByRole('button', { name: '批准' })).toBeVisible()
    await page.getByRole('button', { name: '批准' }).click()
    await expect(page.getByText(/可以在右侧预览/)).toBeVisible()
    await page.getByRole('button', { name: '文件', exact: true }).click()
    await page.getByRole('treeitem', { name: 'note.txt' }).click()
    await expect(page.locator('pre')).toContainText('# Euler 验收')
})

test('慢速流刷新无重复，停止后已取消且刷新保持一致', async ({ page }) => {
    await page.goto('/settings')
    await page.getByRole('button', { name: '新建模型配置' }).click()
    await page.getByLabel('名称').fill('慢速模拟模型')
    await page.getByLabel('Base URL').fill('http://127.0.0.1:18081/v1')
    await page.getByLabel('模型', { exact: true }).fill('euler-e2e')
    await page.getByRole('radio', { name: '环境变量' }).click()
    await page.getByLabel('环境变量名').fill('EULER_TEST_MODEL_KEY')
    await page.getByRole('button', { name: '保存' }).click()

    await page.goto('/')
    await page.getByRole('button', { name: '新建会话' }).click()
    await page.getByRole('button', { name: '创建会话' }).click()
    await page.getByPlaceholder('输入消息').fill('持续输出')
    await page.getByRole('button', { name: '发送' }).click()
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
