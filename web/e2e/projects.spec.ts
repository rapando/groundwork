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
  await page.getByRole('button', { name: `More actions for ${name}` }).click()
  await page.getByRole('menuitem', { name: `Remove ${name}` }).click()
  const dlg = page.getByRole('dialog', { name: `Remove ${name}?` })
  await expect(dlg).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dlg).toHaveCount(0)
  await expect(items).toHaveCount(2)
  await page.getByRole('button', { name: `More actions for ${name}` }).click()
  await page.getByRole('menuitem', { name: `Remove ${name}` }).click()
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

// The Project card in the sidebar opens a menu of the other projects and All projects.
test('the project switcher lists other projects and leads to All projects', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await page.goto(gw.url)
  const switcher = page.getByRole('button', { name: /^Switch project/ })
  await switcher.click()
  const menu = page.getByRole('menu')
  await expect(menu.getByRole('menuitemradio', { checked: true })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
  await expect(switcher).toBeFocused()

  // import a second project from the list
  await switcher.click()
  await menu.getByRole('menuitem', { name: 'All projects' }).click()
  await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible()
  const other = mkdtempSync(path.join(tmpdir(), 'gw-e2e-other-'))
  execFileSync('git', ['init', '-q'], { cwd: other })
  await page.getByPlaceholder(/code\/infra/).fill(other)
  await page.getByRole('button', { name: 'Import folder' }).click()
  await expect(page).toHaveURL(/\/p\/[a-z0-9-]+\//)
  const otherURL = new URL(page.url()).pathname.match(/^\/p\/[a-z0-9-]+\//)![0]

  // back in the first project, the switcher offers the second
  await page.goto(gw.url)
  await switcher.click()
  await expect(menu.getByRole('menuitemradio')).toHaveCount(2)
  await menu.getByRole('menuitemradio', { name: path.basename(other) }).click()
  await expect(page).toHaveURL((u) => u.pathname.startsWith(otherURL))
})

// Typing a path lists the folders it could continue into; arrows and Enter
// or Tab go down one level, and the picked folder is what gets imported.
test('the import field suggests folders as you type a path', async ({ page }) => {
  gw = await start('iac-only')
  await page.goto(gw.url)
  const origin = new URL(gw.url).origin
  await page.goto(origin + '/')
  const parent = mkdtempSync(path.join(tmpdir(), 'gw-e2e-dirs-'))
  execFileSync('git', ['init', '-q', path.join(parent, 'infra-live')])
  execFileSync('mkdir', ['-p', path.join(parent, 'infra-modules'), path.join(parent, 'notes', 'inner'), path.join(parent, '.hidden')])

  const field = page.getByRole('combobox', { name: 'Import' })
  const options = page.getByRole('listbox', { name: 'Folders' }).getByRole('option')
  await field.fill(parent + '/')
  await expect(options).toHaveText([`${parent}/infra-live/git`, `${parent}/infra-modules/`, `${parent}/notes/`])
  await field.fill(parent + '/inf')
  await expect(options).toHaveCount(2)
  await expect(options.first()).toContainText('git')

  // Tab completes the first match; arrows and Enter pick another
  await field.press('Tab')
  await expect(field).toHaveValue(parent + '/infra-live/')
  await field.fill(parent + '/no')
  await expect(options).toHaveText([`${parent}/notes/`])
  await field.press('ArrowDown')
  await expect(options.first()).toHaveAttribute('aria-selected', 'true')
  await field.press('Enter')
  await expect(field).toHaveValue(parent + '/notes/')
  await expect(options).toHaveText([`${parent}/notes/inner/`])
  await field.press('Escape')
  await expect(page.getByRole('listbox', { name: 'Folders' })).toBeHidden()

  // a click picks too, and Import folder imports what's in the field
  await field.fill(parent + '/infra-l')
  await expect(options).toHaveCount(1)
  await options.first().click()
  await expect(field).toHaveValue(parent + '/infra-live/')
  await page.getByRole('button', { name: 'Import folder' }).click()
  await expect(page).toHaveURL(/\/p\/[a-z0-9-]+\/(setup\/scaffold)?$/)
})
