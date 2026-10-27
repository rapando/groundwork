import { test, expect, start, Instance, Page } from './helpers'
import AxeBuilder from '@axe-core/playwright'

let gw: Instance
test.afterEach(async () => {
  await gw?.stop()
})

// axe on every screen: no serious or critical violations. Colour contrast is
// checked too (the themes are designed for it).
const screens = ['/', '/runs', '/graph', '/inventory', '/variables', '/troubleshoot', '/code/terraform/envs/dev/main.tf']

async function scan(page: Page, where: string) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'best-practice']).analyze()
  const bad = results.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
  expect(bad.map((v) => `${where}: ${v.id} (${v.impact}) ${v.help} — ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual([])
}

test('every screen passes axe without serious violations', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await page.goto(gw.url)
  for (const s of screens) {
    await page.goto(gw.base + s)
    await page.waitForLoadState('networkidle')
    await scan(page, s)
  }
  // the project list and a modal
  await page.goto(new URL(gw.url).origin + '/')
  await page.waitForLoadState('networkidle')
  await scan(page, '/projects')
  await page
    .getByRole('button', { name: /^More actions for / })
    .first()
    .click()
  await page.getByRole('menuitem', { name: /^Remove / }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
  await scan(page, '/projects remove dialog')
})

test('the scaffold and detect screens pass axe', async ({ page }) => {
  gw = await start()
  await page.goto(gw.url)
  await expect(page).toHaveURL(/\/setup\/scaffold$/)
  await scan(page, '/setup/scaffold')
  await gw.stop()
  gw = await start('iac-only')
  await page.goto(gw.url)
  await expect(page).toHaveURL(/\/setup\/detect$/)
  await page.waitForLoadState('networkidle')
  await scan(page, '/setup/detect')
})
