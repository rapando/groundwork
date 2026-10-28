import { test, expect, start, Instance, Page } from './helpers'
import { readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const SHOTS = path.resolve(path.dirname(fileURLToPath(import.meta.url)), 'screens')
let gw: Instance
test.afterEach(async () => {
  await gw?.stop()
})

const MAIN = `provider "aws" {
  region = "eu-west-1"
}

resource "aws_vpc" "cluster" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_support   = true
  enable_dns_hostname  = true
}
`

async function open(page: Page, url: string, file: string) {
  await page.goto(url)
  await page.goto(new URL(url).origin + new URL(url).pathname.replace(/\/$/, '') + '/code/' + file)
  await expect(page.locator('.cm-content')).toBeVisible()
}

test('validate → squiggle, badge, one-click fix, save, checks clear on their own', async ({ page }) => {
  gw = await start('iac-only', { init: true, commit: true, files: { 'terraform/envs/dev/main.tf': MAIN } })
  await open(page, gw.url, 'terraform/envs/dev/main.tf')

  await expect(page.locator('.cm-content')).toContainText('enable_dns_hostname')
  await page.getByRole('button', { name: 'Validate' }).click()

  // problem appears in the tray, as a gutter marker, and in the counts
  const tray = page.getByRole('region', { name: 'Problems' })
  await expect(tray.getByText('Unsupported argument')).toBeVisible()
  await expect(page.locator('.cm-lint-marker-error')).toBeVisible()
  await expect(page.getByLabel('1 errors')).toBeVisible()
  await expect(page.locator('.tree', { hasText: 'main.tf' }).getByText('1 err')).toBeVisible()
  await page.screenshot({ path: path.join(SHOTS, 'code-error.png'), fullPage: true })

  // fix edits the buffer only; nothing is written until save
  await tray.getByRole('button', { name: 'Replace with enable_dns_hostnames' }).click()
  await expect(page.locator('.cm-content')).toContainText('enable_dns_hostnames  = true')
  await expect(page.getByText('● modified')).toBeVisible()
  expect(readFileSync(path.join(gw.dir, 'terraform/envs/dev/main.tf'), 'utf8')).toContain('enable_dns_hostname  =')

  await page.keyboard.press('ControlOrMeta+s')
  await expect(page.getByText('Saved · checks will re-run')).toBeVisible()
  expect(readFileSync(path.join(gw.dir, 'terraform/envs/dev/main.tf'), 'utf8')).toContain('enable_dns_hostnames')

  // watcher → on-save checks → SSE → UI, with no click
  await expect(tray.getByText('No problems found.')).toBeVisible({ timeout: 10_000 })
  await expect(page.getByLabel('1 errors')).toHaveCount(0)
  await expect(page.locator('.cm-lint-marker-error')).toHaveCount(0)

  // git sees the change; the diff view shows it
  await expect(page.getByText('● uncommitted')).toBeVisible()
  await page.getByRole('button', { name: 'Diff' }).click()
  await expect(page.getByRole('region', { name: 'Diff' })).toContainText('+  enable_dns_hostnames')
  await page.screenshot({ path: path.join(SHOTS, 'code-diff.png'), fullPage: true })
})

test('external edit reloads a clean buffer; a dirty buffer gets a banner instead', async ({ page }) => {
  gw = await start('iac-only', { init: true, files: { 'terraform/envs/dev/main.tf': MAIN } })
  const file = path.join(gw.dir, 'terraform/envs/dev/main.tf')
  await open(page, gw.url, 'terraform/envs/dev/main.tf')

  writeFileSync(file, MAIN.replace('eu-west-1', 'us-east-2'))
  await expect(page.locator('.cm-content')).toContainText('us-east-2', { timeout: 10_000 })
  await expect(page.getByText('Reloaded: the file changed on disk')).toBeVisible()

  // now make the buffer dirty, then change the file underneath it
  await page.locator('.cm-content').click()
  await page.keyboard.press('ControlOrMeta+End')
  await page.keyboard.type('# my edit')
  await expect(page.getByText('● modified')).toBeVisible()
  writeFileSync(file, MAIN.replace('eu-west-1', 'ap-south-1'))
  await expect(page.getByText('changed on disk while you have unsaved edits')).toBeVisible({ timeout: 10_000 })
  await expect(page.locator('.cm-content')).toContainText('# my edit') // not clobbered

  await page.getByRole('button', { name: 'Reload (discard my edits)' }).click()
  await expect(page.locator('.cm-content')).toContainText('ap-south-1')
  await expect(page.locator('.cm-content')).not.toContainText('# my edit')
  await expect(page.getByText('● modified')).toHaveCount(0)
})

test('saving over a file that changed meanwhile is refused (409), with a way out', async ({ page }) => {
  gw = await start('iac-only', { init: true, files: { 'terraform/envs/dev/main.tf': MAIN } })
  const file = path.join(gw.dir, 'terraform/envs/dev/main.tf')
  await open(page, gw.url, 'terraform/envs/dev/main.tf')

  // delay SSE so the conflict is detected by the save, not by the watcher banner
  await page.route('**/api/events', (r) => r.abort())
  await page.locator('.cm-content').click()
  await page.keyboard.press('ControlOrMeta+End')
  await page.keyboard.type('# mine')
  writeFileSync(file, MAIN + '# theirs\n')
  await page.keyboard.press('ControlOrMeta+s')
  await expect(page.getByText('Saving would overwrite those changes')).toBeVisible()
  expect(readFileSync(file, 'utf8')).toContain('# theirs')
  expect(readFileSync(file, 'utf8')).not.toContain('# mine')

  await page.getByRole('button', { name: 'Overwrite anyway' }).click()
  await expect(page.getByText('Saved · checks will re-run')).toBeVisible()
  expect(readFileSync(file, 'utf8')).toContain('# mine')
})

test('file tree: IaC-only hides unmanaged files, All files shows them; filter narrows', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  await page.goto(gw.url)
  await page.goto(gw.base + '/code')
  const tree = page.getByRole('region', { name: 'Repository files' })
  await expect(tree.getByText('Terraform', { exact: true })).toBeVisible()
  await expect(tree.getByText('Ansible', { exact: true })).toBeVisible()
  await expect(tree.getByText('README.md')).toHaveCount(0)

  await tree.getByRole('button', { name: 'All files' }).click()
  await expect(tree.getByText('README.md')).toBeVisible()

  await tree.getByPlaceholder('Filter files').fill('nginx')
  await expect(tree.getByRole('treeitem', { name: /main\.yml/ })).toBeVisible()
  await expect(tree.getByText('envs/')).toHaveCount(0)
  await page.screenshot({ path: path.join(SHOTS, 'code-tree.png'), fullPage: true })

  // binary and forbidden files get an explanation, not garbage
  await page.goto(gw.base + '/code/.git/config')
  await expect(page.getByText('path is not editable')).toBeVisible()
})

test('editing aids: brackets close, indent guides, completions from sibling files', async ({ page }) => {
  gw = await start('iac-only', {
    init: true,
    commit: true,
    files: {
      'terraform/envs/dev/main.tf': MAIN,
      'terraform/envs/dev/variables.tf': 'variable "instance_type" {\n  type = string\n}\n',
    },
  })
  await open(page, gw.url, 'terraform/envs/dev/main.tf')

  // nested lines get faint guides
  await expect(page.locator('.cm-line.cm-indent-guides').first()).toBeVisible()

  // an opening bracket brings its partner
  await page.locator('.cm-content').click()
  await page.keyboard.press('ControlOrMeta+End')
  await page.keyboard.type('\nlocals {')
  await expect(page.locator('.cm-content')).toContainText('locals {}')
  await page.keyboard.press('Enter')

  // a variable declared in another file of the module is offered
  await page.keyboard.type('size = var.inst')
  const list = page.locator('.cm-tooltip-autocomplete')
  await expect(list.getByText('instance_type')).toBeVisible()
  await page.screenshot({ path: path.join(SHOTS, 'code-complete.png'), fullPage: true })
  await expect(list.locator('li[aria-selected="true"]')).toContainText('instance_type')
  // CodeMirror ignores Enter for interactionDelay (75ms) after the list opens or
  // updates, so a fast keystroke isn't taken as accepting; a slow runner can land in it
  await page.waitForTimeout(150)
  await page.keyboard.press('Enter')
  await expect(page.locator('.cm-content')).toContainText('size = var.instance_type')
})

test('a new file is created from the tree and opened; Delete removes it after a confirmation', async ({ page }) => {
  gw = await start('iac-only', { init: true, commit: true, files: { 'terraform/envs/dev/main.tf': MAIN } })
  await open(page, gw.url, 'terraform/envs/dev/main.tf')

  // starts in the open file's folder; a name that exists is refused
  await page.getByRole('button', { name: 'New file' }).click()
  const dialog = page.getByRole('dialog', { name: 'New file' })
  const input = dialog.getByLabel('Path in the repository')
  await expect(input).toHaveValue('terraform/envs/dev/')
  await input.fill('terraform/envs/dev/main.tf')
  await dialog.getByRole('button', { name: 'Create' }).click()
  await expect(dialog.getByText('A file with that name already exists.')).toBeVisible()

  // a new folder is fine
  await input.fill('terraform/modules/dns/main.tf')
  await dialog.getByRole('button', { name: 'Create' }).click()
  await expect(dialog).toBeHidden()
  await expect(page).toHaveURL(/\/code\/terraform\/modules\/dns\/main\.tf$/)
  await expect(page.locator('[data-path="terraform/modules/dns/main.tf"]')).toBeVisible()
  expect(readFileSync(path.join(gw.dir, 'terraform/modules/dns/main.tf'), 'utf8')).toBe('')

  // never committed, and the confirmation says so; Keep it changes nothing
  await page.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(page.getByText(/never committed/)).toBeVisible()
  await page.getByRole('button', { name: 'Keep it' }).click()
  await page.getByRole('button', { name: 'Delete', exact: true }).click()
  await page.getByRole('button', { name: 'Delete file' }).click()
  await expect(page).toHaveURL(/\/code$/)
  await expect(page.getByText('Deleted terraform/modules/dns/main.tf')).toBeVisible()
  await expect(page.locator('[data-path="terraform/modules/dns/main.tf"]')).toHaveCount(0)
  expect(() => readFileSync(path.join(gw.dir, 'terraform/modules/dns/main.tf'))).toThrow()
})
