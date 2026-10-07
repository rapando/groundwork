import { test, expect, Page } from '@playwright/test'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { start, Instance } from './helpers'

const here = path.dirname(fileURLToPath(import.meta.url))
const SHOTS = path.resolve(here, 'screens')
const DANGER = path.resolve(here, '../../testdata/terraform/show_danger.json')
let gw: Instance
test.afterEach(async () => { await gw?.stop() })

const card = (page: Page, env: string) =>
  page.locator('article').filter({ has: page.locator('.ename', { hasText: new RegExp(`^${env}$`) }) })

async function planAndReview(page: Page, env: string) {
  await page.goto(gw.url)
  await card(page, env).getByRole('button', { name: 'Plan' }).click()
  await expect(page.locator('.dhead .pill', { hasText: 'needs approval' })).toBeVisible({ timeout: 15_000 })
  await page.getByRole('link', { name: 'Review plan and approve →' }).click()
  await expect(page).toHaveURL(/\/runs\/\d+\/plan$/)
}

function stateFile(serial: number) {
  const p = path.join(mkdtempSync(path.join(tmpdir(), 'gw-state-')), 'state.json')
  writeFileSync(p, JSON.stringify({ serial, lineage: 'L' }))
  return p
}

test('a database replacement needs an acknowledgement and the typed environment name', async ({ page }) => {
  gw = await start('iac-only', { init: true, env: { FAKE_SHOW_JSON: DANGER } })
  await planAndReview(page, 'prod')

  await expect(page.getByRole('alert').filter({ hasText: 'aws_db_instance.main will be destroyed and recreated.' })).toBeVisible()
  await expect(page.getByRole('alert').filter({ hasText: 'aws_s3_bucket.logs[1] will be destroyed.' })).toBeVisible()

  // the most consequential change opens first, with the attribute that forces it on top
  const diff = page.getByRole('region', { name: 'Resource diff' })
  await expect(diff.getByText('aws_db_instance.main', { exact: true })).toBeVisible()
  await expect(diff.getByText('must be replaced')).toBeVisible()
  await expect(diff.locator('.df.forces').first()).toContainText('triggers_replace')
  await expect(diff.locator('.df.forces').first()).toContainText('# forces replacement')
  await expect(page.getByText('Why it\'s replaced')).toBeVisible()
  await page.screenshot({ path: path.join(SHOTS, 'plan-danger.png'), fullPage: true })

  // filters
  await page.getByRole('button', { name: 'Destroy 1' }).click()
  const list = page.getByRole('region', { name: 'Resources in plan' })
  await expect(list.locator('.res')).toHaveCount(1)
  await list.locator('.res').click()
  await expect(diff.getByText('will be destroyed')).toBeVisible()
  await page.getByRole('button', { name: /^All/ }).click()

  const apply = page.getByRole('button', { name: /^Apply plan #/ })
  await expect(apply).toBeDisabled()
  await page.getByLabel('Type prod to confirm').fill('prod')
  await expect(apply).toBeDisabled() // still needs the acknowledgement
  await page.getByLabel(/I understand 2 data-bearing resources/).check()
  await expect(apply).toBeEnabled()
  await apply.click()
  await expect(page).toHaveURL(/\/runs\/\d+$/)
  await expect(page.getByText('Applied: 3 added')).toBeVisible({ timeout: 20_000 })
})

test('a state change after planning is caught at apply time; re-plan recovers', async ({ page }) => {
  const sf = stateFile(7)
  gw = await start('iac-only', { init: true, env: { FAKE_STATE_FILE: sf } })
  await planAndReview(page, 'dev')
  await expect(page.getByText(/state serial 7/)).toBeVisible()

  writeFileSync(sf, JSON.stringify({ serial: 8, lineage: 'L' })) // someone applied elsewhere
  await page.getByRole('button', { name: /^Apply plan #/ }).click()
  const stale = page.getByRole('alert').filter({ hasText: 'Re-plan required.' })
  await expect(stale).toBeVisible()
  await expect(stale).toContainText('the state changed since the plan (serial 7 → 8)')
  await expect(page).toHaveURL(/\/plan$/) // nothing was applied
  await page.screenshot({ path: path.join(SHOTS, 'plan-stale.png'), fullPage: true })

  const oldUrl = page.url()
  await stale.getByRole('button', { name: 'Re-plan now' }).click()
  await expect(page).not.toHaveURL(oldUrl)
  await expect(page.locator('.dhead .pill', { hasText: 'needs approval' })).toBeVisible({ timeout: 15_000 })
  await page.getByRole('link', { name: 'Review plan and approve →' }).click()
  await expect(page.getByText(/state serial 8/)).toBeVisible()
  await page.getByRole('button', { name: /^Apply plan #/ }).click()
  await expect(page.getByText('Applied: 3 added')).toBeVisible({ timeout: 20_000 })
})

test('editing the configuration after planning marks the plan stale before you try', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await planAndReview(page, 'dev')
  await expect(page.getByRole('button', { name: /^Apply plan #/ })).toBeEnabled()

  writeFileSync(path.join(gw.dir, 'terraform/envs/dev/main.tf'), '# changed after planning\n')
  await page.reload()
  await expect(page.getByRole('alert').filter({ hasText: 'Re-plan required.' })).toContainText('configuration changed')
  await expect(page.getByRole('button', { name: /^Apply plan #/ })).toBeDisabled()
})
