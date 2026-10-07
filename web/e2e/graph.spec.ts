import { test, expect, Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { start, Instance } from './helpers'

const SHOTS = path.resolve(path.dirname(fileURLToPath(import.meta.url)), 'screens')
let gw: Instance
test.afterEach(async () => { await gw?.stop() })

const origin = () => new URL(gw.url).origin

async function login(page: Page) {
  await page.goto(gw.url)
  await expect(page.getByRole('link', { name: 'Overview', exact: true })).toBeVisible()
}

test('the architecture view follows unsaved edits, with cursor sync both ways', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await login(page)
  await page.goto(origin() + '/code/terraform/modules/network/main.tf')
  await expect(page.locator('.cm-content')).toBeVisible()
  await page.getByRole('button', { name: 'Split' }).click()

  const arch = page.getByRole('region', { name: 'Infrastructure view' })
  await expect(arch.getByText('module.network', { exact: true })).toBeVisible()
  await expect(arch.getByRole('img', { name: /2 nodes, 1 edges/ })).toBeVisible({ timeout: 10_000 })
  await expect(arch.locator('text', { hasText: 'aws_vpc.main' })).toBeVisible() // the VPC is drawn as a container
  await expect(arch.getByText('Not placed')).toHaveCount(0)
  await page.screenshot({ path: path.join(SHOTS, 'code-split.png'), fullPage: true })

  // cursor in the subnet block → that box is outlined
  await page.locator('.cm-line', { hasText: 'cidr_block = cidrsubnet' }).click()
  await expect(arch.locator('svg rect[stroke="#B8F36B"]')).toHaveCount(1)

  // remove the subnet's vpc_id in the buffer, without saving
  await page.locator('.cm-line', { hasText: 'vpc_id     = aws_vpc.main.id' }).click()
  await page.keyboard.press('Home')
  await page.keyboard.press('Shift+End')
  await page.keyboard.press('Backspace')
  await expect(page.getByText('● modified')).toBeVisible()
  await expect(arch.getByRole('img', { name: /2 nodes, 0 edges/ })).toBeVisible({ timeout: 10_000 })
  expect(readFileSync(path.join(gw.dir, 'terraform/modules/network/main.tf'), 'utf8')).toContain('vpc_id     = aws_vpc.main.id')

  // clicking a box moves the editor to its definition
  // activate the container from the keyboard (its area overlaps the boxes laid out around it)
  await arch.getByRole('button', { name: /aws_vpc\.main \(contains/ }).focus()
  await page.keyboard.press('Enter')
  await expect(page.locator('.status')).toContainText('Ln 1,')
})

test('drift: detect, see it in Graph and on the Overview, copy the real value into code', async ({ page }) => {
  gw = await start('iac-only', {
    init: true,
    files: { 'terraform/envs/dev/files.tf': 'resource "local_file" "motd" {\n  filename        = "motd.txt"\n  content         = "hello"\n  file_permission = "0644"\n}\n' },
  })
  await login(page)
  await page.getByRole('link', { name: 'Graph', exact: true }).click()
  await page.getByRole('combobox', { name: 'Stack' }).selectOption({ label: 'dev / terraform/envs/dev' })
  await page.getByRole('button', { name: 'Detect drift' }).click()

  await expect(page).toHaveURL(/\/runs\/\d+$/)
  await expect(page.locator('.banner', { hasText: /Drift: 1 resource\(s\) differ/ })).toBeVisible({ timeout: 20_000 })
  await page.getByRole('link', { name: 'Review in Graph →' }).click()

  const node = page.getByRole('button', { name: /local_file motd, drift/ })
  await expect(node).toBeVisible({ timeout: 10_000 })
  await node.click({ force: true })
  const insp = page.getByRole('region', { name: 'Selected resource' })
  await expect(insp.getByText(/drifted · detected/)).toBeVisible()
  const row = insp.locator('tr', { hasText: 'content' })
  await expect(row).toContainText('"hello"')
  await expect(row).toContainText('"changed outside"')
  await page.screenshot({ path: path.join(SHOTS, 'graph-drift.png'), fullPage: true })

  // copy the real value into code: preview first, then apply
  await row.getByRole('button', { name: 'Copy into code' }).click()
  const dlg = page.getByRole('dialog')
  await expect(dlg.getByRole('region', { name: 'Diff' })).toContainText('+  content         = "changed outside"')
  expect(readFileSync(path.join(gw.dir, 'terraform/envs/dev/files.tf'), 'utf8')).toContain('"hello"')
  await dlg.getByRole('button', { name: 'Apply change' }).click()
  await expect(page).toHaveURL(/\/code\/terraform\/envs\/dev\/files\.tf/)
  expect(readFileSync(path.join(gw.dir, 'terraform/envs/dev/files.tf'), 'utf8')).toContain('"changed outside"')

  // the overview shows the environment as drifted
  await page.getByRole('link', { name: 'Overview', exact: true }).click()
  const dev = page.locator('article').filter({ has: page.locator('.ename', { hasText: /^dev$/ }) })
  await expect(dev.getByText('drift', { exact: true })).toBeVisible()
  await expect(dev.getByRole('link', { name: '1' })).toBeVisible()
  await expect(page.getByRole('complementary', { name: 'Sidebar' }).getByText('drift')).toBeVisible()
})

test('dependency graph renders and switches to the modules view', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await login(page)
  await page.goto(origin() + '/graph?root=terraform/envs/prod&env=prod')
  const g = page.getByRole('region', { name: 'Dependency graph' })
  await expect(g.getByText(/2 resources · 1 edges · from static/)).toBeVisible()
  await expect(g.getByRole('button', { name: /aws_subnet private/ })).toBeVisible()
  await page.getByRole('button', { name: 'Modules' }).click()
  await expect(g.getByRole('button', { name: /module network/ })).toBeVisible()
  const dl = page.waitForEvent('download')
  await g.getByRole('button', { name: 'Export SVG' }).click()
  const file = await (await dl).path()
  expect(readFileSync(file!, 'utf8')).toContain('<svg')
})
