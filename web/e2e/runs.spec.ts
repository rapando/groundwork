import { test, expect, Page } from '@playwright/test'
import { start, Instance } from './helpers'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const SHOTS = path.resolve(path.dirname(fileURLToPath(import.meta.url)), 'screens')
let gw: Instance
test.afterEach(async () => { await gw?.stop() })

async function overview(page: Page, url: string) {
  await page.goto(url)
  await expect(page.getByRole('region', { name: 'Environments' })).toBeVisible()
}
// the run header's status pill (not the "Needs approval" filter button or list entries)
const waitForApproval = (page: Page) =>
  expect(page.locator('.dhead .pill', { hasText: 'needs approval' })).toBeVisible({ timeout: 30_000 })
const applyRunning = (page: Page) => expect(page.locator('li.stage.running', { hasText: 'apply' })).toBeVisible({ timeout: 10_000 })

// approve through the plan review screen
async function approveVia(page: Page, confirm = '') {
  await page.getByRole('link', { name: 'Review plan and approve →' }).click()
  await expect(page).toHaveURL(/\/runs\/\d+\/plan$/)
  if (confirm) await page.getByLabel(`Type ${confirm} to confirm`).fill(confirm)
  await page.getByRole('button', { name: /^Apply plan #/ }).click()
  await expect(page).toHaveURL(/\/runs\/\d+$/)
}

const card = (page: Page, env: string) =>
  page.locator('article').filter({ has: page.locator('.ename', { hasText: new RegExp(`^${env}$`) }) })

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
  await expect(changes.getByText('module.service.terraform_data.service[0]')).toBeVisible()
  await expect(changes.getByText('will be created')).toHaveCount(3)
  await page.screenshot({ path: path.join(SHOTS, 'run-plan.png'), fullPage: true })

  // dev needs no typed confirmation
  await approveVia(page)

  // resources go running → done while the log streams in
  await expect(changes.getByText('running').first()).toBeVisible({ timeout: 10_000 })
  await expect(page.getByRole('log').getByText(/Creating\.\.\./).first()).toBeVisible()
  await page.screenshot({ path: path.join(SHOTS, 'run-applying.png'), fullPage: true })
  await expect(page.getByText('Applied: 3 added, 0 changed, 0 destroyed')).toBeVisible({ timeout: 20_000 })
  await expect(changes.getByText('done')).toHaveCount(3)
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
  page.on('dialog', (d) => d.accept())
  await overview(page, gw.url)
  await card(page, 'dev').getByRole('button', { name: 'Plan' }).click()
  await waitForApproval(page)
  await approveVia(page)
  await applyRunning(page)

  const t0 = Date.now()
  await page.getByRole('button', { name: 'Cancel run' }).click()
  await expect(page.locator('.dhead .pill', { hasText: 'cancelled' })).toBeVisible({ timeout: 8_000 })
  expect(Date.now() - t0).toBeLessThan(8_000)
  await expect(page.getByRole('button', { name: 'Cancel run' })).toHaveCount(0)
})

test('log search and level filter narrow what is shown', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await overview(page, gw.url)
  await card(page, 'dev').getByRole('button', { name: 'Plan' }).click()
  await waitForApproval(page)
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
  await page.goto(new URL(gw.url).origin + '/?t=' + new URL(gw.url).searchParams.get('t'))
  await page.goto(new URL(gw.url).origin + runUrl.pathname)
  await waitForApproval(page)
  await approveVia(page, 'prod')
  await expect(page.getByText('Applied: 3 added')).toBeVisible({ timeout: 20_000 })

  // second run: crash the server mid-apply
  gw = await gw.restart({ FAKE_APPLY_DELAY: '3' })
  await page.goto(new URL(gw.url).origin + '/?t=' + new URL(gw.url).searchParams.get('t'))
  await page.goto(new URL(gw.url).origin + '/')
  await card(page, 'dev').getByRole('button', { name: 'Plan' }).click()
  await waitForApproval(page)
  const crashed = page.url()
  await approveVia(page)
  await applyRunning(page)
  gw.kill()
  await new Promise((r) => setTimeout(r, 300))

  gw = await gw.restart()
  await page.goto(new URL(gw.url).origin + '/?t=' + new URL(gw.url).searchParams.get('t'))
  await page.goto(new URL(gw.url).origin + new URL(crashed).pathname)
  await expect(page.locator('.dhead .pill', { hasText: 'failed' })).toBeVisible()
  await expect(page.getByText(/interrupted: groundwork stopped/)).toBeVisible()
})
