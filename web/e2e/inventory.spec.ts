import { test, expect, Page } from '@playwright/test'
import { existsSync, mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { start, Instance } from './helpers'

const SHOTS = path.resolve(path.dirname(fileURLToPath(import.meta.url)), 'screens')
let gw: Instance
test.afterEach(async () => { await gw?.stop() })

// real ansible against the lab: local-connection hosts plus one unreachable ssh host
async function lab(page: Page) {
  const labDir = mkdtempSync(path.join(tmpdir(), 'gw-lab-'))
  gw = await start('ansible-lab', { init: true, files: { 'group_vars/all.yml': `---\nlab_dir: ${labDir}\n` } })
  await page.goto(gw.url)
  await page.getByRole('link', { name: 'Inventory', exact: true }).click()
  await expect(page.getByRole('region', { name: 'Hosts' })).toBeVisible()
  return labDir
}
const hostRow = (page: Page, name: string) => page.getByRole('region', { name: 'Hosts' }).locator('.hr', { has: page.getByRole('button', { name, exact: true }) })

test('ping marks reachability; variables show their winning source', async ({ page }) => {
  await lab(page)
  await expect(page.getByRole('region', { name: 'Hosts' }).locator('.hr:not(.head)')).toHaveCount(4)
  await page.getByRole('button', { name: 'Ping all' }).click()
  await expect(hostRow(page, 'web-1').getByText(/up · \d+ms/)).toBeVisible({ timeout: 30_000 })
  await expect(hostRow(page, 'down-1').getByText('unreachable')).toBeVisible()
  await expect(page.getByRole('region', { name: 'Groups' }).getByText('1 · 1 down')).toBeVisible()

  await hostRow(page, 'web-2').getByRole('button', { name: 'web-2' }).click()
  const detail = page.getByRole('region', { name: 'Host detail' })
  const nw = detail.locator('tbody', { hasText: 'nginx_workers' })
  await expect(nw.locator('tr.first')).toContainText('3')
  await expect(nw.locator('tr.first')).toContainText('group_vars/web.yml')
  await expect(nw.locator('tr.ov')).toHaveCount(3) // inventory group_vars, inventory file, role default
  await expect(nw.locator('tr.ov').first()).toContainText('inventory/group_vars/web.yml')
  await page.screenshot({ path: path.join(SHOTS, 'inventory.png'), fullPage: true })
})

test('playbook: dry run per host, approve, run for real; last play updates', async ({ page }) => {
  const labDir = await lab(page)
  await hostRow(page, 'web-1').getByRole('checkbox').check()
  await page.getByRole('button', { name: 'Run playbook on 1 host' }).click()
  await page.getByText('Dry run, then run after approval').click()
  await page.getByRole('button', { name: 'Start' }).click()

  await expect(page).toHaveURL(/\/runs\/\d+$/)
  await expect(page.locator('.dhead .pill', { hasText: 'needs approval' })).toBeVisible({ timeout: 30_000 })
  const hosts = page.getByRole('region', { name: 'Hosts' })
  await expect(hosts.locator('.change', { hasText: 'web-1' }).locator('.pill', { hasText: 'changed' })).toBeVisible()
  await hosts.getByRole('button', { name: /web-1/ }).click()
  await expect(hosts.locator('.tdiff').first()).toContainText('workers=4')
  expect(existsSync(path.join(labDir, 'web-1.conf'))).toBe(false) // the dry run changed nothing
  await page.screenshot({ path: path.join(SHOTS, 'ansible-dryrun.png'), fullPage: true })

  await hosts.getByRole('button', { name: 'Run for real' }).click()
  await expect(page.locator('.dhead .pill', { hasText: 'succeeded' })).toBeVisible({ timeout: 30_000 })
  expect(readFileSync(path.join(labDir, 'web-1.conf'), 'utf8')).toBe('workers=4 port=8080\n')

  await page.getByRole('link', { name: 'Inventory', exact: true }).click()
  await expect(hostRow(page, 'web-1').getByText(/#\d+ ok \d+ · chg 1/)).toBeVisible()
})

test('a full play reports changed, failed and unreachable hosts', async ({ page }) => {
  await lab(page)
  await page.getByRole('button', { name: 'Run playbook' }).click()
  await page.getByRole('button', { name: 'Start' }).click() // dry run only (default)
  await expect(page.locator('.dhead .pill', { hasText: 'failed' })).toBeVisible({ timeout: 30_000 })
  const hosts = page.getByRole('region', { name: 'Hosts' })
  await expect(hosts.locator('.change', { hasText: 'down-1' }).locator('.pill', { hasText: 'unreachable' })).toBeVisible()
  await expect(hosts.locator('.change', { hasText: 'web-2' }).locator('.pill', { hasText: 'failed' })).toBeVisible()
  await expect(hosts.locator('.change', { hasText: 'web-1' }).locator('.pill', { hasText: 'changed' })).toBeVisible()
  await expect(page.getByText(/1 host\(s\) failed: web-2; 1 unreachable: down-1/)).toBeVisible()
})

test('a mutating ad-hoc command needs the environment typed', async ({ page }) => {
  await lab(page)
  await page.getByRole('button', { name: 'Ad-hoc command…' }).click()
  const dlg = page.getByRole('dialog')
  await dlg.getByLabel('Hosts (pattern)').fill('web-1') // 'all' would include the deliberately unreachable host
  await dlg.getByLabel('Module').fill('command')
  await dlg.getByLabel('Arguments').fill('echo hi')
  await expect(dlg.getByRole('button', { name: 'Run' })).toBeDisabled()
  await dlg.getByLabel('Type dev to confirm').fill('dev')
  await dlg.getByRole('button', { name: 'Run' }).click()
  await expect(page.locator('.dhead .pill', { hasText: 'succeeded' })).toBeVisible({ timeout: 30_000 })
})

test('Overview: an Ansible environment opens the playbook dialog', async ({ page }) => {
  gw = await start('ansible-lab', { init: true })
  await page.goto(gw.url)
  const dev = page.locator('article.env', { has: page.getByText('dev', { exact: true }) })
  await dev.getByRole('button', { name: 'Run playbook' }).click()
  await expect(page).toHaveURL(/\/inventory\?project=\.&env=dev$/)
  await expect(page.getByRole('heading', { name: 'Run playbook · dev' })).toBeVisible()
})
