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

test('structured edits: add, change and remove hosts, groups and variables, each previewed', async ({ page }) => {
  const hosts = '---\n# dev inventory\np2p:\n  children:\n    dev:\n      hosts:\n        p2p-dev:\n          ansible_connection: local\n\nlocal:\n  hosts: {}\n'
  gw = await start(undefined, { files: {
    'groundwork.yaml': 'version: 1\nmode: standalone\nansible:\n  projects:\n    - path: .\n      inventories:\n        dev: environments/dev/hosts.yml\n',
    'environments/dev/hosts.yml': hosts,
    'site.yml': '- hosts: all\n  gather_facts: false\n  tasks:\n    - ansible.builtin.ping:\n',
  } })
  const file = (p: string) => readFileSync(path.join(gw.dir, p), 'utf8')
  const apply = async (contains: string) => {
    const dlg = page.getByRole('dialog')
    await expect(dlg.locator('.diff')).toContainText(contains)
    await dlg.getByRole('button', { name: 'Apply' }).click()
    await expect(dlg).toHaveCount(0)
  }
  const hostsList = page.getByRole('region', { name: 'Hosts' })
  const vars = page.getByRole('region', { name: 'Host detail' })
  await page.goto(gw.url)
  await page.getByRole('link', { name: 'Inventory', exact: true }).click()
  await expect(hostsList.getByRole('button', { name: 'p2p-dev', exact: true })).toBeVisible()

  // a host, into the empty group, with an address
  await page.getByRole('button', { name: 'Add host' }).click()
  await page.getByRole('dialog').getByLabel('Host name').fill('web-3')
  await page.getByRole('dialog').getByRole('combobox', { name: /^Group/ }).selectOption('local')
  await page.getByRole('dialog').getByLabel('Address (optional)').fill('10.0.1.13')
  await page.getByRole('button', { name: 'Preview' }).click()
  await apply('+    web-3:')
  await expect(hostsList.getByRole('button', { name: 'web-3', exact: true })).toBeVisible()
  expect(file('environments/dev/hosts.yml')).toContain('local:\n  hosts:\n    web-3:\n      ansible_host: 10.0.1.13\n')

  // a variable in a new host_vars file, then changed, then removed
  await hostsList.getByRole('button', { name: 'web-3', exact: true }).click()
  await vars.getByRole('button', { name: '+ Add variable' }).click()
  await page.getByRole('dialog').getByLabel('Name').fill('app_port')
  await page.getByRole('dialog').getByLabel('Value (YAML)').fill('8080')
  await page.getByRole('dialog').getByLabel('Write to').selectOption('environments/dev/host_vars/web-3.yml')
  await page.getByRole('button', { name: 'Preview' }).click()
  await apply('+app_port: 8080')
  const row = vars.locator('tr.first', { hasText: 'app_port' })
  await expect(row).toContainText('8080')
  await expect(row).toContainText('environments/dev/host_vars/web-3.yml')
  await row.getByRole('button', { name: 'Edit app_port' }).click()
  await page.getByRole('dialog').getByLabel('Value (YAML)').fill('9090')
  await page.getByRole('button', { name: 'Preview' }).click()
  await apply('+app_port: 9090')
  await expect(row).toContainText('9090')
  await row.getByRole('button', { name: 'Remove app_port' }).click()
  await apply('-app_port: 9090')
  await expect(vars.locator('tr.first', { hasText: 'app_port' })).toHaveCount(0)

  // secrets are refused
  await vars.getByRole('button', { name: '+ Add variable' }).click()
  await page.getByRole('dialog').getByLabel('Name').fill('db_password')
  await page.getByRole('dialog').getByLabel('Value (YAML)').fill('hunter2')
  await page.getByRole('button', { name: 'Preview' }).click()
  await expect(page.getByRole('dialog').getByRole('alert')).toContainText('looks like a secret')
  await page.getByRole('button', { name: 'Cancel' }).click()

  // a group, then removed again (only empty groups can be)
  await page.getByRole('region', { name: 'Groups' }).getByRole('button', { name: '+ Add' }).click()
  await page.getByRole('dialog').getByLabel('Group name').fill('cache')
  await page.getByRole('dialog').getByLabel('Inside').selectOption('p2p')
  await page.getByRole('button', { name: 'Preview' }).click()
  await apply('+    cache: {}')
  const groups = page.getByRole('region', { name: 'Groups' })
  await groups.getByRole('button', { name: /^cache/ }).click()
  await groups.getByRole('button', { name: 'Remove group cache' }).click()
  await apply('-    cache: {}')
  await expect(groups.getByRole('button', { name: /^cache/ })).toHaveCount(0)

  // the host, from the whole inventory
  await groups.getByRole('button', { name: /^all/ }).click()
  await hostsList.getByRole('button', { name: 'web-3', exact: true }).click()
  await vars.getByRole('button', { name: 'Remove…' }).click()
  await page.getByRole('button', { name: 'Preview' }).click()
  await apply('-    web-3:')
  await expect(hostsList.getByRole('button', { name: 'web-3', exact: true })).toHaveCount(0)

  // every edit undone: the inventory is byte-for-byte what it was
  expect(file('environments/dev/hosts.yml')).toBe(hosts)
})
