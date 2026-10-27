import { test, expect, Page, start, Instance } from './helpers'
import { existsSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const SHOTS = path.resolve(path.dirname(fileURLToPath(import.meta.url)), 'screens')
let gw: Instance
test.afterEach(async () => {
  await gw?.stop()
})

async function overview(page: Page, url: string) {
  await page.goto(url)
  await expect(page.getByRole('region', { name: 'Environments' })).toBeVisible()
}
// the run header's status pill (not the "Needs approval" filter button or list entries)
const waitForApproval = (page: Page) => expect(page.locator('.dhead .pill', { hasText: 'needs approval' })).toBeVisible({ timeout: 30_000 })
const applyRunning = (page: Page) => expect(page.locator('li.stage.running', { hasText: 'apply' })).toBeVisible({ timeout: 10_000 })

// approve through the plan review screen
async function approveVia(page: Page, confirm = '') {
  await page.getByRole('link', { name: 'Review plan and approve →' }).click()
  await expect(page).toHaveURL(/\/runs\/\d+\/plan$/)
  if (confirm) await page.getByLabel(`Type ${confirm} to confirm`).fill(confirm)
  await page.getByRole('button', { name: /^Apply plan #/ }).click()
  await expect(page).toHaveURL(/\/runs\/\d+$/)
}

const card = (page: Page, env: string) => page.locator('article').filter({ has: page.locator('.ename', { hasText: new RegExp(`^${env}$`) }) })

test('plan from the overview, review, approve, watch the apply stream live', async ({ page }) => {
  gw = await start('iac-only', { init: true, env: { FAKE_APPLY_DELAY: '1' } })
  await overview(page, gw.url)

  await expect(card(page, 'dev').getByText('not planned yet')).toBeVisible()
  await card(page, 'dev').getByRole('button', { name: 'Plan' }).click()

  // the run page: stages progress to "waiting for approval" with the plan listed
  await expect(page).toHaveURL(/\/runs\/\d+$/)
  await expect(page.getByRole('heading', { name: /terraform plan/ })).toBeVisible()
  await waitForApproval(page)
  const changes = page.getByRole('region', { name: 'Planned changes' })
  // the Changes tab opens first: one tile per resource, grouped by module
  await expect(page.getByRole('tab', { name: /Changes/ })).toHaveAttribute('aria-selected', 'true')
  await expect(changes.getByText('module.service', { exact: true })).toBeVisible()
  await expect(changes.getByRole('button', { name: /terraform_data service\[0\]/ })).toBeVisible()
  await expect(changes.getByText('planned', { exact: true })).toHaveCount(3)
  await page.screenshot({ path: path.join(SHOTS, 'run-plan.png'), fullPage: true })

  // dev needs no typed confirmation
  await approveVia(page)

  // resources go running → done while the log streams in
  await expect(changes.getByText(/^running/).first()).toBeVisible({ timeout: 10_000 })
  await page.screenshot({ path: path.join(SHOTS, 'run-applying.png'), fullPage: true })
  // a tile opens the log searched for that resource
  await changes.getByRole('button', { name: /terraform_data service\[0\]/ }).click()
  await expect(page.getByRole('tab', { name: /Log/ })).toHaveAttribute('aria-selected', 'true')
  await expect(page).toHaveURL(/tab=log/)
  await expect(page.getByLabel('Search log')).toHaveValue('module.service.terraform_data.service[0]')
  await expect(
    page
      .getByRole('log')
      .getByText(/Creating\.\.\./)
      .first(),
  ).toBeVisible()
  await page.getByLabel('Search log').fill('')
  await page.getByRole('tab', { name: /Changes/ }).click()
  await expect(page.getByText('Applied: 3 added, 0 changed, 0 destroyed')).toBeVisible({ timeout: 20_000 })
  await expect(changes.getByText('done', { exact: true })).toHaveCount(3)
  await page.screenshot({ path: path.join(SHOTS, 'run-done.png'), fullPage: true })

  // the overview reflects it
  await page.getByRole('link', { name: 'Overview', exact: true }).click()
  await expect(card(page, 'dev').getByText(/apply #\d+ succeeded/)).toBeVisible()
})

test('prod needs the environment name typed before approval is possible', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await overview(page, gw.url)
  await card(page, 'prod').getByRole('button', { name: 'Plan' }).click()
  await waitForApproval(page)

  await page.getByRole('link', { name: 'Review plan and approve →' }).click()
  const approve = page.getByRole('button', { name: /^Apply plan #/ })
  await expect(approve).toBeDisabled()
  await page.getByLabel('Type prod to confirm').fill('dev')
  await expect(approve).toBeDisabled()
  await page.getByLabel('Type prod to confirm').fill('prod')
  await expect(approve).toBeEnabled()
  await approve.click()
  await expect(page.getByText('Applied: 3 added')).toBeVisible({ timeout: 20_000 })
})

test('cancelling mid-apply stops terraform promptly', async ({ page }) => {
  gw = await start('iac-only', { init: true, env: { FAKE_APPLY_DELAY: '10' } })
  await overview(page, gw.url)
  await card(page, 'dev').getByRole('button', { name: 'Plan' }).click()
  await waitForApproval(page)
  await approveVia(page)
  await applyRunning(page)

  // while it runs, choose to hear back only if it fails
  const notify = page.getByRole('button', { name: 'Notify me about this run' })
  await notify.click()
  await page.getByRole('menuitemradio', { name: /Only if it fails/ }).click()
  await expect(notify).toContainText('Only if it fails')
  await notify.click()
  await expect(page.getByRole('menuitemradio', { name: /Only if it fails/ })).toHaveAttribute('aria-checked', 'true')
  await page.keyboard.press('Escape')
  await expect(page.getByRole('menu')).toHaveCount(0)
  await expect(notify).toBeFocused()

  // cancelling asks on the page first; keeping it running changes nothing
  await page.getByRole('button', { name: 'Cancel run' }).click()
  const ask = page.getByRole('group', { name: /^Cancel this run\?/ })
  await expect(ask).toBeVisible()
  await ask.getByRole('button', { name: 'Keep running' }).click()
  await expect(ask).toHaveCount(0)
  await page.getByRole('button', { name: 'Cancel run' }).click()
  const t0 = Date.now()
  await ask.getByRole('button', { name: 'Cancel the run' }).click()
  await expect(page.locator('.dhead .pill', { hasText: 'cancelled' })).toBeVisible({ timeout: 8_000 })
  expect(Date.now() - t0).toBeLessThan(8_000)
  await expect(page.getByRole('button', { name: 'Cancel run' })).toHaveCount(0)
})

test('a failed plan explains itself: the known cause, the lock holder and the next step', async ({ page }) => {
  const lock = path.join(mkdtempSync(path.join(tmpdir(), 'gw-lock-')), 'locked')
  writeFileSync(lock, '')
  gw = await start('iac-only', { init: true, env: { FAKE_LOCKED: lock } })
  await overview(page, gw.url)
  await card(page, 'dev').getByRole('button', { name: 'Plan' }).click()
  await expect(page.locator('.dhead .pill', { hasText: 'failed' })).toBeVisible({ timeout: 15_000 })

  // a finish time instead of a counter
  await expect(page.locator('.dhead .sub')).toContainText(/failed \d\d:\d\d:\d\d after \d+/)
  const cause = page.getByRole('region', { name: 'State locked on terraform/envs/dev (dev)' })
  await expect(cause).toBeVisible({ timeout: 10_000 })
  await expect(cause.getByText('known cause')).toBeVisible()
  await expect(cause.getByText('dev@workstation.local', { exact: true })).toBeVisible()
  await expect(cause.getByText('none: it stopped before applying anything')).toBeVisible()
  await expect(cause.getByRole('button', { name: 'Plan again' })).toBeVisible()
  await expect(page.getByRole('tab', { name: /Log/ })).toContainText(/\d+ error/)

  // with no plan to draw, the page opens on the log, where the raw error is
  await expect(page.getByRole('tab', { name: /Log/ })).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('tab', { name: /Changes/ })).toHaveAttribute('aria-disabled', 'true')
  await expect(page.getByRole('log').getByText(/eb4ca245-50b1-5658-c0c3-a4c69baa2888/)).toBeVisible()
  await page.screenshot({ path: path.join(SHOTS, 'run-failed.png'), fullPage: true })

  // "Show in log" opens the log on its errors
  await cause.getByRole('button', { name: 'Show in log' }).click()
  await expect(page.getByRole('tab', { name: /Log/ })).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('button', { name: /^Error/ })).toHaveAttribute('aria-pressed', 'true')

  // releasing the lock asks on the page, with the typed environment
  await cause.getByRole('button', { name: 'Release the lock…' }).click()
  const ask = cause.getByRole('group', { name: 'Release the lock?' })
  const unlock = ask.getByRole('button', { name: 'Force unlock' })
  await expect(unlock).toBeDisabled()
  await ask.getByLabel('Type dev to confirm').fill('dev')
  await unlock.click()
  await expect(page.locator('.dhead .pill', { hasText: 'succeeded' })).toBeVisible({ timeout: 15_000 })
  expect(existsSync(lock)).toBe(false)
})

test('log search and level filter narrow what is shown', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await overview(page, gw.url)
  await card(page, 'dev').getByRole('button', { name: 'Plan' }).click()
  await waitForApproval(page)
  await page.getByRole('tab', { name: /Log/ }).click()
  const log = page.getByRole('log')
  await expect(log.getByText('Terraform has been successfully initialized!')).toBeVisible()

  await page.getByLabel('Search log').fill('Plan to create')
  await expect(log.getByText('Terraform has been successfully initialized!')).toHaveCount(0)
  await expect(log.getByText(/Plan to create/)).toHaveCount(3)

  await page.getByLabel('Search log').fill('')
  await page.getByRole('button', { name: /^Error/ }).click()
  await expect(log.getByText('No log output.')).toBeVisible()
})

test('a plan waiting for approval survives a restart; a crashed apply is marked interrupted', async ({ page }) => {
  test.setTimeout(120_000) // three server lifetimes and two applies
  gw = await start('iac-only', { init: true })
  await overview(page, gw.url)
  await card(page, 'prod').getByRole('button', { name: 'Plan' }).click()
  await waitForApproval(page)
  const runUrl = new URL(page.url())

  gw = await gw.restart()
  await page.goto(gw.url)
  await page.goto(new URL(gw.url).origin + runUrl.pathname)
  await waitForApproval(page)
  await approveVia(page, 'prod')
  await expect(page.getByText('Applied: 3 added')).toBeVisible({ timeout: 20_000 })

  // second run: crash the server mid-apply
  gw = await gw.restart({ FAKE_APPLY_DELAY: '3' })
  await page.goto(gw.url)
  await page.goto(gw.base + '/')
  await card(page, 'dev').getByRole('button', { name: 'Plan' }).click()
  await waitForApproval(page)
  const crashed = page.url()
  await approveVia(page)
  await applyRunning(page)
  gw.kill()
  await new Promise((r) => setTimeout(r, 300))

  gw = await gw.restart()
  await page.goto(gw.url)
  await page.goto(new URL(gw.url).origin + new URL(crashed).pathname)
  await expect(page.locator('.dhead .pill', { hasText: 'failed' })).toBeVisible()
  await expect(page.getByText(/interrupted: groundwork stopped/)).toBeVisible()
})

test('a streaming log follows the newest line; scrolling up pauses, the bottom resumes', async ({ page }) => {
  const tasks = Array.from({ length: 60 }, (_, i) => `    - name: step ${i}\n      ansible.builtin.command: sleep 0.15\n      changed_when: false\n`).join('')
  gw = await start(undefined, {
    init: true,
    files: {
      'ansible.cfg': '[defaults]\ninventory = inventory/dev.yml\n',
      'inventory/dev.yml': 'all:\n  children:\n    web:\n      hosts:\n        web-1:\n          ansible_connection: local\n',
      'site.yml': `- hosts: web\n  gather_facts: false\n  tasks:\n${tasks}`,
    },
  })
  // short enough that the header and a fixed log floor would overflow the window
  await page.setViewportSize({ width: 1280, height: 560 })
  await page.goto(gw.url)
  await page.getByRole('link', { name: 'Inventory', exact: true }).click()
  await page.getByRole('button', { name: 'Run playbook' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Start' }).click()
  await page.waitForURL(/\/runs\/\d+/)
  await page.getByRole('tab', { name: /Log/ }).click()
  const log = page.getByRole('log', { name: 'Log output' })
  const pageTop = () => page.evaluate(() => document.scrollingElement!.scrollTop)
  const follow = page.getByRole('checkbox', { name: 'Follow' })
  const gap = () => log.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight)
  const height = () => log.evaluate((el) => el.scrollHeight)

  // the log outgrows its box while staying pinned to the bottom
  await expect.poll(height, { timeout: 30_000 }).toBeGreaterThan(900)
  expect(await gap()).toBeLessThan(8)

  // scrolling down a following log moves neither it nor the page around it
  await log.hover()
  await page.mouse.wheel(0, 600)
  await expect(follow).toBeChecked()
  expect(await pageTop()).toBe(0)

  await page.mouse.wheel(0, -600)
  await expect(follow).not.toBeChecked()
  const h = await height()
  await expect.poll(height).toBeGreaterThan(h + 100) // more lines arrive…
  expect(await gap()).toBeGreaterThan(100) // …without moving the reader

  await log.evaluate((el) => {
    el.scrollTop = el.scrollHeight
  })
  await expect(follow).toBeChecked()
})

test('destroy is a plan: confirmed, reviewed, then approved by typing the environment', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await overview(page, gw.url)

  await card(page, 'dev').getByRole('button', { name: 'More actions for dev' }).click()
  await card(page, 'dev').getByRole('menuitem', { name: 'Destroy…' }).click()
  const confirm = card(page, 'dev').getByRole('group', { name: 'Plan the destruction of everything in dev?' })
  await expect(confirm).toContainText('Nothing is destroyed yet')
  await confirm.getByRole('button', { name: 'Keep it' }).click()
  await expect(confirm).toHaveCount(0)

  await card(page, 'dev').getByRole('button', { name: 'More actions for dev' }).click()
  await card(page, 'dev').getByRole('menuitem', { name: 'Destroy…' }).click()
  await confirm.getByRole('button', { name: 'Plan destroy' }).click()
  await expect(page).toHaveURL(/\/runs\/\d+$/)
  await expect(page.getByRole('heading', { name: /terraform plan -destroy/ })).toBeVisible()
  await waitForApproval(page)

  await page.getByRole('link', { name: 'Review plan and approve →' }).click()
  await expect(page.getByRole('heading', { name: /destroy plan · dev/ })).toBeVisible()
  await expect(page.getByRole('note').filter({ hasText: 'destroys every resource' })).toBeVisible()
  // even dev, which needs no typing for a plan, needs its name typed for a destroy
  const apply = page.getByRole('button', { name: /^Apply plan #/ })
  await expect(apply).toBeDisabled()
  await page.getByLabel('Type dev to confirm').fill('dev')
  const ack = page.getByRole('checkbox', { name: /^I understand/ })
  if (await ack.count()) await ack.check()
  await expect(apply).toBeEnabled()
  await apply.click()
  await expect(page).toHaveURL(/\/runs\/\d+$/)
  await expect(page.getByRole('heading', { name: /terraform destroy/ })).toBeVisible({ timeout: 20_000 })
})
