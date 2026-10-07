import { test, expect, Page } from '@playwright/test'
import { existsSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { start, Instance } from './helpers'

const SHOTS = path.resolve(path.dirname(fileURLToPath(import.meta.url)), 'screens')
let gw: Instance
test.afterEach(async () => { await gw?.stop() })

const card = (page: Page, env: string) =>
  page.locator('article').filter({ has: page.locator('.ename', { hasText: new RegExp(`^${env}$`) }) })

test('a stale state lock: explained on the Overview, fixed from Troubleshoot', async ({ page }) => {
  const lock = path.join(mkdtempSync(path.join(tmpdir(), 'gw-lock-')), 'locked')
  writeFileSync(lock, '')
  gw = await start('iac-only', { init: true, env: { FAKE_LOCKED: lock } })
  await page.goto(gw.url)
  await card(page, 'dev').getByRole('button', { name: 'Plan' }).click()
  await expect(page.locator('.dhead .pill', { hasText: 'failed' })).toBeVisible({ timeout: 15_000 })

  // the Overview lists it under Needs attention
  await page.getByRole('link', { name: 'Overview', exact: true }).click()
  const attention = page.getByRole('region', { name: 'Needs attention' })
  await expect(attention.getByText('State locked on terraform/envs/dev (dev)')).toBeVisible({ timeout: 10_000 })
  await expect(page.getByRole('link', { name: /Troubleshoot\s*1/ })).toBeVisible()
  await page.screenshot({ path: path.join(SHOTS, 'overview.png'), fullPage: true })
  await attention.getByText('State locked on terraform/envs/dev (dev)').click()

  // detail: lock facts, automated pre-checks, the command, a typed confirmation
  const detail = page.getByRole('region', { name: 'Issue detail' })
  await expect(detail.getByRole('heading', { name: 'State locked on terraform/envs/dev (dev)' })).toBeVisible()
  await expect(detail.getByText('eb4ca245-50b1-5658-c0c3-a4c69baa2888').first()).toBeVisible()
  await expect(detail.getByText(/^Checked: No groundwork job is running .* No terraform process is running/)).toBeVisible()
  await expect(detail.locator('code', { hasText: 'force-unlock -force eb4ca245' })).toBeVisible()
  const unlock = detail.getByRole('button', { name: 'Force unlock' })
  await expect(unlock).toBeDisabled()
  await detail.getByLabel('Type dev to confirm').fill('dev')
  await page.screenshot({ path: path.join(SHOTS, 'troubleshoot.png'), fullPage: true })
  await unlock.click()

  await expect(page).toHaveURL(/\/runs\/\d+$/)
  await expect(page.locator('.dhead .pill', { hasText: 'succeeded' })).toBeVisible({ timeout: 15_000 })
  expect(existsSync(lock)).toBe(false)

  // resolved by the successful unlock
  await page.getByRole('link', { name: /^Troubleshoot/ }).click()
  await expect(page.getByText('0 open · 1 resolved this week')).toBeVisible({ timeout: 10_000 })
  await expect(page.getByRole('region', { name: 'Open issues' }).getByText(/fixed: run #\d+ \(tf.unlock\) succeeded/)).toBeVisible()

  // doctor runs from the same screen
  await page.getByRole('button', { name: 'Run doctor' }).click()
  const doctor = page.getByRole('region', { name: 'Doctor' })
  await expect(doctor.getByText('Disk space')).toBeVisible({ timeout: 30_000 })
  await expect(doctor.getByText(/ran just now/)).toBeVisible()
})

test('command palette and keyboard shortcuts', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await page.goto(gw.url)
  await expect(page.getByRole('region', { name: 'Environments' })).toBeVisible()

  await page.keyboard.press('g')
  await page.keyboard.press('r')
  await expect(page).toHaveURL(/\/runs$/)

  await page.keyboard.press('ControlOrMeta+k')
  const pal = page.getByRole('dialog', { name: 'Command palette' })
  await expect(pal).toBeVisible()
  await pal.getByLabel('Search commands').fill('main.tf network')
  await expect(pal.getByRole('option').first()).toContainText('terraform/modules/network/main.tf')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/code\/terraform\/modules\/network\/main\.tf$/)

  // typing in the editor never triggers shortcuts
  await page.locator('.cm-content').click()
  await page.keyboard.type('g')
  await page.keyboard.type('r')
  await expect(page).toHaveURL(/main\.tf$/)
  await page.keyboard.press('ControlOrMeta+z')

  await page.locator('body').press('ControlOrMeta+k')
  await pal.getByLabel('Search commands').fill('plan dev')
  await expect(pal.getByRole('option').first()).toContainText('Plan dev')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/runs\/\d+$/)
  await expect(page.locator('.dhead .pill', { hasText: 'needs approval' })).toBeVisible({ timeout: 15_000 })

  await page.locator('body').press('?')
  await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toHaveCount(0)
})

test('first run: the tour points at each area, the checklist follows real activity', async ({ page }) => {
  gw = await start('iac-only', { init: true, tips: true })
  await page.goto(gw.url)
  const tip = page.getByRole('dialog', { name: 'Tips' })
  await expect(tip.getByText('Tip 1 of 6')).toBeVisible()
  await expect(tip.getByText(/found \d+ environments?, \d+ Terraform roots?/)).toBeVisible()
  await page.screenshot({ path: path.join(SHOTS, 'tour.png') })
  await tip.getByRole('button', { name: 'Show me around' }).click()
  await expect(tip.getByText('Tip 2 of 6')).toBeVisible()
  // a ring is drawn around the area each tip talks about
  const ringAround = async (anchor: string) => {
    const ring = page.locator(`.tour-ring[data-anchor="${anchor}"]`)
    await expect(ring).toBeVisible()
    await expect.poll(async () => {
      const a = (await page.locator(`[data-tour="${anchor}"]`).first().boundingBox())!
      const r = (await ring.boundingBox())!
      return Math.abs(r.x + 6 - a.x) < 3 && Math.abs(r.y + 6 - a.y) < 3 && Math.abs(r.width - 12 - a.width) < 3
    }).toBe(true)
  }
  await ringAround('nav')
  for (const anchor of ['envs', 'checks', 'issues', 'header']) {
    await tip.getByRole('button', { name: 'Next' }).click()
    await ringAround(anchor)
    // the tip stays on screen
    const box = (await tip.boundingBox())!
    expect(box.y).toBeGreaterThanOrEqual(0)
    expect(box.y + box.height).toBeLessThanOrEqual(1100)
  }
  await tip.getByRole('button', { name: 'Done' }).click()
  await expect(tip).toHaveCount(0)
  await page.reload()
  await expect(page.getByRole('region', { name: 'Environments' })).toBeVisible()
  await expect(page.getByRole('dialog', { name: 'Tips' })).toHaveCount(0) // remembered

  const list = page.getByRole('region', { name: 'Getting started' })
  const item = (label: RegExp) => list.getByRole('listitem').filter({ hasText: label })
  await expect(item(/Scan the repository/).locator('.tick')).toHaveText('✓')
  await expect(item(/Run doctor/).locator('.tick')).not.toHaveText('✓')
  await expect(item(/first plan/).locator('.tick')).not.toHaveText('✓')

  // doing the things ticks them off
  await card(page, 'dev').getByRole('button', { name: 'Plan' }).click()
  await expect(page).toHaveURL(/\/runs\/\d+$/)
  await page.goto(new URL(gw.url).origin + '/code/terraform/modules/network/main.tf')
  await page.getByRole('button', { name: 'Split' }).click()
  await expect(page.getByRole('region', { name: 'Infrastructure view' })).toBeVisible()
  await page.goto(new URL(gw.url).origin + '/troubleshoot?doctor=1')
  await expect(page.getByRole('region', { name: 'Doctor' }).getByText(/ran just now/)).toBeVisible({ timeout: 30_000 })
  await page.getByRole('link', { name: 'Overview', exact: true }).click()
  for (const l of [/first plan/, /infrastructure view/, /Run doctor/]) await expect(item(l).locator('.tick')).toHaveText('✓')

  // the ? button replays the tour; Hide removes the checklist for good
  await page.getByRole('button', { name: 'Show tips' }).click()
  await expect(page.getByRole('dialog', { name: 'Tips' })).toBeVisible()
  await page.getByRole('button', { name: 'Close tips' }).click()
  await list.getByRole('button', { name: 'Hide' }).click()
  await expect(list).toHaveCount(0)
  await page.reload()
  await expect(page.getByRole('region', { name: 'Environments' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Getting started' })).toHaveCount(0)
})
