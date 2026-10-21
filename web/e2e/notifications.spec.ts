import { test, expect, Page, start, Instance } from './helpers'
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const SHOTS = path.resolve(path.dirname(fileURLToPath(import.meta.url)), 'screens')
let gw: Instance
test.afterEach(async () => {
  await gw?.stop()
})

// opens a page away from the run and waits for the event stream
async function elsewhere(page: Page, url: string) {
  await page.goto(url)
  await expect(page.getByRole('link', { name: 'Notifications', exact: true })).toBeVisible()
  await expect(page.getByText(/connecting…|reconnecting…/)).toHaveCount(0)
}

// starts a plan from the page itself, so the browser stays where it is
async function planFromAPI(page: Page, env: string) {
  await page.evaluate(async (env) => {
    const token = document.querySelector<HTMLMetaElement>('meta[name="groundwork-token"]')!.content
    const id = location.pathname.match(/^\/p\/([^/]+)/)![1]
    const res = await fetch(`/api/p/${id}/runs`, {
      method: 'POST',
      headers: { 'X-Groundwork-Token': token, 'Content-Type': 'application/json' },
      body: JSON.stringify({ kind: 'tf.plan', root: `terraform/envs/${env}`, env }),
    })
    if (!res.ok) throw new Error('plan: ' + res.status)
  }, env)
}

const toasts = (page: Page) => page.getByRole('list', { name: 'New notifications' }).getByRole('listitem')

test('a run that fails while you edit raises a toast, the bell and the Runs badge', async ({ page }) => {
  const lock = path.join(mkdtempSync(path.join(tmpdir(), 'gw-lock-')), 'locked')
  writeFileSync(lock, '')
  gw = await start('iac-only', { init: true, env: { FAKE_LOCKED: lock } })
  await elsewhere(page, gw.url + 'code')
  await planFromAPI(page, 'dev')

  // the toast says what failed, offers the Troubleshoot fix first, and stays
  const toast = toasts(page).filter({ hasText: /Plan #\d+ failed on dev/ })
  await expect(toast).toBeVisible({ timeout: 20_000 })
  await expect(toast.getByRole('link').first()).toHaveAttribute('href', /\/troubleshoot\/\d+$/)
  await expect(toast.getByRole('link', { name: 'Open run' })).toBeVisible()
  await expect(page.getByRole('link', { name: /^Runs\s*1 failed$/ })).toBeVisible()
  const bell = page.getByRole('button', { name: 'Notifications, 1 unread' })
  await expect(bell).toBeVisible()
  await page.screenshot({ path: path.join(SHOTS, 'notify-failed.png'), fullPage: true })
  await page.waitForTimeout(6500) // failures don't fade like successes do
  await expect(toast).toBeVisible()

  // the bell lists it under Today; Escape closes the list and focus returns
  await bell.click()
  const inbox = page.getByRole('dialog', { name: 'Notifications' })
  await expect(inbox.getByRole('region', { name: 'Today' }).getByRole('link', { name: /Plan #\d+ failed on dev/ })).toBeVisible()
  await expect(inbox.getByRole('link', { name: 'Notification settings →' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(inbox).toHaveCount(0)
  await expect(bell).toBeFocused()

  // opening the run reads it: the toast, the badge and the count clear
  await toast.getByRole('link', { name: 'Open run' }).click()
  await expect(page).toHaveURL(/\/runs\/\d+$/)
  await expect(toasts(page)).toHaveCount(0)
  await expect(page.getByRole('link', { name: /^Runs\s*1 failed$/ })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Notifications', exact: true })).toBeVisible()
})

test('a plan waiting for approval asks for you without an approve button', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await elsewhere(page, gw.url + 'code')
  await planFromAPI(page, 'dev')

  const toast = toasts(page).filter({ hasText: /Plan #\d+ is ready for review/ })
  await expect(toast).toBeVisible({ timeout: 30_000 })
  await expect(toast.getByRole('link', { name: 'Review plan' })).toHaveAttribute('href', /\/runs\/\d+\/plan$/)
  await expect(toast.getByRole('button', { name: /approve/i })).toHaveCount(0)
  await expect(page.getByRole('link', { name: /^Runs\s*1 waiting for approval$/ })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Review the plan waiting for approval on dev' })).toHaveText('approve')
  await page.screenshot({ path: path.join(SHOTS, 'notify-approval.png'), fullPage: true })

  // Later dismisses the toast; the entry stays in the bell
  await toast.getByRole('button', { name: 'Later' }).click()
  await expect(toasts(page)).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Notifications, 1 unread' })).toBeVisible()

  // the project list shows what needs you, with the service-wide bell
  await page.goto(new URL(gw.url).origin + '/')
  await expect(page.getByRole('list', { name: 'Projects' }).getByText('1 waiting')).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: 'Notifications, 1 unread' }).click()
  const inbox = page.getByRole('dialog', { name: 'Notifications' })
  await expect(inbox.getByRole('link', { name: /Plan #\d+ is ready for review/ })).toBeVisible()
  await inbox.getByRole('button', { name: 'Mark all read' }).click()
  await expect(page.getByRole('button', { name: 'Notifications', exact: true })).toBeVisible()
})

test('notification settings write the notify block and send a test', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await page.goto(gw.url)
  await page.keyboard.press('ControlOrMeta+k')
  await page.getByLabel('Search commands').fill('notifications')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/notifications$/)
  await expect(page.getByRole('heading', { name: 'Notifications', level: 1 })).toBeVisible()

  // the defaults: failures everywhere, approvals on the desktop, successes in the console only
  await expect(page.getByLabel('Run failed: Console toast')).toBeChecked()
  await expect(page.getByLabel('Run failed: Desktop')).toBeChecked()
  await expect(page.getByLabel('Waiting for approval: Desktop')).toBeChecked()
  await expect(page.getByLabel('Run succeeded: Console toast')).toBeChecked()
  await expect(page.getByLabel('Run succeeded: Desktop')).not.toBeChecked()
  await expect(page.getByRole('button', { name: 'Allow desktop notifications in this browser' })).toBeVisible()

  // a project page edits this project's overrides; rows it doesn't change follow all projects
  await expect(page.getByRole('radio', { name: /This project/ })).toBeChecked()
  await expect(page.getByRole('row', { name: /Run failed/ })).toContainText('All projects')
  const save = page.getByRole('button', { name: 'Save overrides' })
  await expect(save).toBeDisabled()
  await page.getByLabel('Run succeeded: Desktop').check()
  await expect(page.getByRole('row', { name: /Run succeeded/ })).toContainText('This project')
  await page.getByLabel('Skip runs shorter than').selectOption('2m')
  await save.click()
  await expect(page.getByText('Saved to groundwork.yaml', { exact: true })).toBeVisible()
  await expect(page.getByText('2 overrides in groundwork.yaml')).toBeVisible()
  const yaml = readFileSync(path.join(gw.dir, 'groundwork.yaml'), 'utf8')
  expect(yaml).toMatch(/^notify:\n {2}success: \[console, desktop\]\n {2}quiet_under: 2m\n/m)
  expect(yaml).not.toMatch(/failed:/)

  await page.reload()
  await expect(page.getByLabel('Run succeeded: Desktop')).toBeChecked()
  await expect(page.getByLabel('Skip runs shorter than')).toHaveValue('2m')

  // a bad webhook URL is refused with the reason
  await page.getByLabel('Webhook URL').fill('not a url')
  await save.click()
  await expect(page.getByRole('alert')).toBeVisible()

  await page.reload()
  await page.getByRole('button', { name: 'Send a test' }).click()
  await expect(page.locator('form .result')).toHaveText(/Test sent|failed|Nothing to test/, { timeout: 15_000 })
  await page.screenshot({ path: path.join(SHOTS, 'notify-settings.png'), fullPage: true })
})

test('settings for every project apply under a project, which overrides them key by key', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await page.goto(gw.url + 'notifications')
  await page.getByRole('radio', { name: 'All projects' }).click()
  await expect(page).toHaveURL(/scope=all/)
  await expect(page.getByText('These are the defaults.', { exact: false })).toBeVisible()
  await page.getByLabel('Webhook URL').fill('https://hooks.example.com/services/mine')
  await page.getByLabel('Drift found: Webhook').check()
  await page.getByLabel('Add an environment').fill('prod')
  await page.getByLabel('Add an environment').press('Enter')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page.getByText('Saved for every project', { exact: true })).toBeVisible()
  const machine = readFileSync(path.join(gw.home, 'notify.yaml'), 'utf8')
  expect(machine).toMatch(/drift: \[console, desktop, webhook\]/)
  expect(machine).toMatch(/webhook: https:\/\/hooks.example.com\/services\/mine/)
  expect(readFileSync(path.join(gw.dir, 'groundwork.yaml'), 'utf8')).not.toMatch(/notify:/)

  // the project follows them: the hook stays out of the repository
  await page.getByRole('radio', { name: /This project/ }).click()
  await expect(page.getByLabel('Drift found: Webhook')).toBeChecked()
  await expect(page.getByLabel('Webhook URL')).toHaveAttribute('placeholder', 'https://hooks.example.com/services/mine')
  await expect(page.getByText('Nothing overridden: this project follows all projects.')).toBeVisible()

  // an override, then reset back to following
  await page.getByLabel('Drift found: Webhook').uncheck()
  await page.getByRole('button', { name: 'Save overrides' }).click()
  await expect(page.getByText('1 override in groundwork.yaml')).toBeVisible()
  expect(readFileSync(path.join(gw.dir, 'groundwork.yaml'), 'utf8')).toMatch(/notify:\n {2}drift: \[console, desktop\]\n/)
  await page.screenshot({ path: path.join(SHOTS, 'notify-overrides.png'), fullPage: true })
  await page.getByRole('button', { name: 'Reset Drift found to all projects' }).click()
  await expect(page.getByLabel('Drift found: Webhook')).toBeChecked()
  await page.getByRole('button', { name: 'Save overrides' }).click()
  await expect(page.getByText('Following all projects', { exact: true })).toBeVisible()
  expect(readFileSync(path.join(gw.dir, 'groundwork.yaml'), 'utf8')).not.toMatch(/notify:/)

  // the project list reaches the same settings from the bell
  await page.goto(new URL('/', gw.url).toString())
  await page.getByRole('button', { name: /^Notifications/ }).click()
  await page.getByRole('link', { name: 'Notification settings →' }).click()
  await expect(page).toHaveURL(/\/notifications$/)
  await expect(page.getByRole('radiogroup', { name: 'Settings for' })).toHaveCount(0)
  await expect(page.getByLabel('Drift found: Webhook')).toBeChecked()
  await expect(page.getByRole('link', { name: '← Projects' })).toBeVisible()
})
