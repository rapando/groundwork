import { test, expect } from '@playwright/test'
import { start, Instance } from './helpers'

let gw: Instance
test.afterEach(async () => { await gw?.stop() })

test('paper by default; dark is one click away, remembered, and recolours the editor', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await page.goto(gw.url)
  const html = page.locator('html')
  await expect(html).toHaveAttribute('data-theme', 'paper')
  const bg = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  expect(await bg()).toBe('rgb(246, 243, 235)')

  await page.getByRole('radio', { name: 'Dark' }).click()
  await expect(html).toHaveAttribute('data-theme', 'dark')
  expect(await bg()).toBe('rgb(13, 16, 18)')

  // survives a reload, and the editor follows it
  await page.goto(gw.base + '/code/terraform/envs/dev/main.tf')
  await expect(html).toHaveAttribute('data-theme', 'dark')
  await expect(page.locator('.cm-editor').first()).toBeVisible()
  const editorBg = () => page.locator('.cm-editor').first().evaluate((e) => getComputedStyle(e).backgroundColor)
  const dark = await editorBg()
  await page.getByRole('radio', { name: 'Paper' }).click()
  await expect.poll(editorBg).not.toBe(dark)
  await expect(page.getByRole('radio', { name: 'Paper' })).toHaveAttribute('aria-checked', 'true')
})
