import { test, expect, start, Instance } from './helpers'
import { mkdtempSync, renameSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { execFileSync } from 'node:child_process'

let gw: Instance
test.afterEach(async () => {
  await gw?.stop()
})

// The project list outside /p/<id>/: import a folder, remove it, and bring
// back one whose folder went missing.
test('projects page imports, removes and reopens projects', async ({ page }) => {
  gw = await start('iac-only')
  await page.goto(gw.url) // signs in
  const origin = new URL(gw.url).origin
  await page.goto(origin + '/')
  await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible()
  await expect(page.getByRole('list', { name: 'Projects' }).getByRole('listitem')).toHaveCount(1)

  // import a second repository by path
  const other = mkdtempSync(path.join(tmpdir(), 'gw-e2e-other-'))
  execFileSync('git', ['init', '-q'], { cwd: other })
  await page.getByPlaceholder(/code\/infra/).fill(other)
  await page.getByRole('button', { name: 'Import folder' }).click()
  await expect(page).toHaveURL(/\/p\/[a-z0-9-]+\/(setup\/scaffold)?$/) // opened in the new project
  await page.goto(origin + '/')
  const items = page.getByRole('list', { name: 'Projects' }).getByRole('listitem')
  await expect(items).toHaveCount(2)
  const name = path.basename(other)
  await expect(page.getByText(name, { exact: true })).toBeVisible()

  // remove it: the dialog traps focus and Escape cancels
  await page.getByRole('button', { name: `Remove ${name}` }).click()
  const dlg = page.getByRole('dialog', { name: `Remove ${name}?` })
  await expect(dlg).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dlg).toHaveCount(0)
  await expect(items).toHaveCount(2)
  await page.getByRole('button', { name: `Remove ${name}` }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Remove project' }).click()
  await expect(items).toHaveCount(1)

  // a project whose folder disappears is unavailable, and comes back with Retry
  const moved = gw.dir + '-moved'
  renameSync(gw.dir, moved)
  gw = await gw.restart() // a new port: sign in again, then the project list
  await page.goto(gw.url)
  await page.goto(new URL(gw.url).origin + '/')
  await expect(page.getByText('unavailable')).toBeVisible()
  renameSync(moved, gw.dir)
  await page.getByRole('button', { name: 'Retry' }).click()
  await expect(page.getByText('unavailable')).toHaveCount(0)
  await expect(page.getByRole('link', { name: 'Open' })).toBeVisible()
})
