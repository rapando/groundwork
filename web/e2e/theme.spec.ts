import { test, expect, start, Instance } from './helpers'

let gw: Instance
test.afterEach(async () => {
  await gw?.stop()
})

test('paper by default; dark is one click away, remembered, and recolours the editor', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await page.goto(gw.url)
  const html = page.locator('html')
  await expect(html).toHaveAttribute('data-theme', 'paper')
  const bg = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  expect(await bg()).toBe('rgb(247, 245, 240)')

  await page.getByRole('radio', { name: 'Dark' }).click()
  await expect(html).toHaveAttribute('data-theme', 'dark')
  expect(await bg()).toBe('rgb(13, 16, 18)')

  // survives a reload, and the editor follows it
  await page.goto(gw.base + '/code/terraform/envs/dev/main.tf')
  await expect(html).toHaveAttribute('data-theme', 'dark')
  await expect(page.locator('.cm-editor').first()).toBeVisible()
  const editorBg = () =>
    page
      .locator('.cm-editor')
      .first()
      .evaluate((e) => getComputedStyle(e).backgroundColor)
  const dark = await editorBg()
  await page.getByRole('radio', { name: 'Paper' }).click()
  await expect.poll(editorBg).not.toBe(dark)
  await expect(page.getByRole('radio', { name: 'Paper' })).toHaveAttribute('aria-checked', 'true')
})

test('the accent switches between green and orange in either theme, and is remembered', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await page.goto(gw.url)
  const html = page.locator('html')
  const accent = () => page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--accent').trim().toLowerCase())
  await expect(html).toHaveAttribute('data-accent', 'green')
  expect(await accent()).toBe('#3b5d10')

  await page.getByRole('radio', { name: 'Orange accent' }).click()
  await expect(html).toHaveAttribute('data-accent', 'orange')
  expect(await accent()).toBe('#9a3a07')

  await page.getByRole('radio', { name: 'Dark' }).click()
  expect(await accent()).toBe('#ff8b3d')

  await page.reload()
  await expect(html).toHaveAttribute('data-accent', 'orange')
  await expect(page.getByRole('radio', { name: 'Orange accent' })).toHaveAttribute('aria-checked', 'true')
})
