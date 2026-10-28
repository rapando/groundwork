import { test, expect, start, Instance } from './helpers'
import { existsSync, mkdtempSync, readFileSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const SHOTS = path.resolve(path.dirname(fileURLToPath(import.meta.url)), 'screens')
let gw: Instance
test.afterEach(async () => {
  await gw?.stop()
})

test('requests without a session are rejected', async ({ request }) => {
  gw = await start('embedded')
  const origin = new URL(gw.url).origin
  const api = origin + '/api' + new URL(gw.base).pathname
  expect((await request.get(api + '/workspace')).status()).toBe(401)
  expect((await request.get(api + '/events')).status()).toBe(401)
  expect((await request.get(origin + '/api/projects')).status()).toBe(401)
  // pages load (as the sign-in screen) but never carry the API token
  for (const u of [gw.base + '/', origin + '/']) {
    const res = await request.get(u)
    expect(res.status()).toBe(200)
    expect(await res.text()).not.toContain('groundwork-token')
  }
})

test('empty repo: setup asks for the vault password, writes only the glue, unlocks the vault', async ({ page }) => {
  gw = await start()
  await page.goto(gw.url)
  await expect(page).toHaveURL(/\/setup\/scaffold$/)
  await expect(page.getByRole('heading', { name: 'Set up this repo' })).toBeVisible()
  // no provider to pick: the Terraform is yours
  await expect(page.getByRole('group', { name: 'Provider' })).toHaveCount(0)
  await expect(page.getByText(/^\+3 files/)).toBeVisible()

  // Create files waits for the vault password
  const create = page.getByRole('button', { name: 'Create files' })
  await expect(create).toBeDisabled()
  await expect(page.getByText('Set the ansible-vault password in step 4 to continue.')).toBeVisible()
  await page.getByLabel('Vault password').fill('e2e-vault-pw')
  await page.getByLabel('Repeat it').fill('e2e-vault-px')
  await expect(page.getByText("The passwords don't match.")).toBeVisible()
  await expect(create).toBeDisabled()
  await page.getByLabel('Repeat it').fill('e2e-vault-pw')
  await expect(page.getByText('✓ Passwords match')).toBeVisible()
  await expect(create).toBeEnabled()

  // preview shows the inventory script and never writes
  await page.getByRole('button', { name: 'tf_outputs.py', exact: true }).click()
  await expect(page.locator('.viewer pre')).toContainText('ansible_hosts')
  expect(existsSync(path.join(gw.dir, 'groundwork.yaml'))).toBe(false)
  await page.screenshot({ path: path.join(SHOTS, 'scaffold.png'), fullPage: true })

  await page.getByText('Workspaces', { exact: true }).click()
  await create.click()
  await expect(page.getByRole('heading', { name: 'Repository set up' })).toBeVisible()
  await expect(page.getByText(/Vault password set/)).toBeVisible()
  const cfg = readFileSync(path.join(gw.dir, 'groundwork.yaml'), 'utf8')
  expect(cfg).toContain('workspace: prod')
  expect(cfg).not.toContain('vault_password_file')
  expect(cfg).not.toContain('e2e-vault-pw')
  expect(existsSync(path.join(gw.dir, 'ansible/inventory/tf_outputs.py'))).toBe(true)
  expect(existsSync(path.join(gw.dir, 'terraform'))).toBe(false)

  // Add a secret opens New secret on an unlocked vault
  await page.getByRole('button', { name: 'Add a secret' }).click()
  const dlg = page.getByRole('dialog', { name: 'New secret' })
  await expect(dlg).toBeVisible()
  await expect(page.getByRole('region', { name: 'Secrets' }).getByText('Vault unlocked', { exact: true })).toBeVisible()
  await dlg.getByLabel('File, from the repository root').fill('ansible/group_vars/prod/vault.yml')
  await dlg.getByLabel('Variable name').fill('db_password')
  await dlg.getByLabel('Value').fill('Db-Pw-1')
  await dlg.getByRole('button', { name: 'Encrypt and save' }).click()
  await expect(dlg).toHaveCount(0)
  expect(readFileSync(path.join(gw.dir, 'ansible/group_vars/prod/vault.yml'), 'utf8')).toContain('db_password: !vault |')
})

test('empty repo: a password file is created outside the repo, or reused', async ({ page }) => {
  const pwFile = path.join(mkdtempSync(path.join(tmpdir(), 'gw-e2e-pw-')), '.vault-pass')
  gw = await start()
  await page.goto(gw.url)
  await page.getByRole('radio', { name: 'Password file' }).check()
  await page.getByLabel(/^Password file, under/).fill('.vault-pass')
  await expect(page.getByText(/outside the repository/)).toBeVisible()
  await page.getByLabel(/^Password file, under/).fill(pwFile)
  await expect(page.getByText(/^\+4 files/)).toBeVisible()
  await expect(page.getByText(pwFile, { exact: true })).toBeVisible() // listed outside the repo
  await page.getByLabel('Vault password').fill('file-pw')
  await page.getByLabel('Repeat it').fill('file-pw')
  await page.getByRole('button', { name: 'Create files' }).click()
  await expect(page.getByRole('heading', { name: 'Repository set up' })).toBeVisible()
  expect(readFileSync(pwFile, 'utf8')).toBe('file-pw\n')
  expect(statSync(pwFile).mode & 0o777).toBe(0o600)
  expect(readFileSync(path.join(gw.dir, 'groundwork.yaml'), 'utf8')).toContain('vault_password_file: ' + pwFile)
  await gw.stop()

  // another repo: the existing file is used, nothing to type
  gw = await start()
  await page.goto(gw.url)
  await page.getByRole('radio', { name: 'Password file' }).check()
  await page.getByLabel(/^Password file, under/).fill(pwFile)
  await expect(page.getByText(/This file exists/)).toBeVisible()
  await expect(page.getByLabel('Vault password')).toHaveCount(0)
  await page.getByRole('button', { name: 'Create files' }).click()
  await expect(page.getByText(/Using the vault password in/)).toBeVisible()
  expect(readFileSync(pwFile, 'utf8')).toBe('file-pw\n')
})

test('empty repo: only the chosen tools are set up', async ({ page }) => {
  gw = await start()
  await page.goto(gw.url)
  const tools = page.getByRole('group', { name: /What will this repository use/ })
  const tf = tools.getByRole('checkbox', { name: /^Terraform/ })
  const tofu = tools.getByRole('checkbox', { name: /^OpenTofu/ })
  const ans = tools.getByRole('checkbox', { name: /^Ansible/ })
  await expect(tf).toBeChecked()
  await expect(ans).toBeChecked()

  // OpenTofu replaces Terraform: the inventory script runs tofu
  await tofu.check()
  await expect(tf).not.toBeChecked()
  await expect(page.getByRole('heading', { name: '4 · Ansible inventory from OpenTofu' })).toBeVisible()
  await page.getByRole('button', { name: 'tf_outputs.py', exact: true }).click()
  await expect(page.locator('.viewer pre')).toContainText('"tofu", "terraform", "tofu"')
  await expect(page.getByText('tofu fmt', { exact: true })).toBeVisible()

  // Ansible alone: ansible.cfg, no environments, no inventory script
  await tofu.uncheck()
  await expect(page.getByRole('heading', { name: '3 · Ansible', exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: /Environments/ })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'tf_outputs.py', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'ansible.cfg', exact: true })).toBeVisible()

  // nothing ticked is refused
  await ans.uncheck()
  await expect(page.getByText('choose Terraform, OpenTofu or Ansible')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Create files' })).toBeDisabled()

  // OpenTofu alone: groundwork.yaml only, no vault password to set
  await tofu.check()
  await expect(page.getByText(/^\+1 files/)).toBeVisible()
  await expect(page.getByLabel('Vault password')).toHaveCount(0)
  await expect(page.getByText('groundwork init --tools tofu')).toBeVisible()
  await page.getByRole('button', { name: 'Create files' }).click()
  await expect(page.getByRole('heading', { name: 'Repository set up' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add a secret' })).toHaveCount(0)
  const cfg = readFileSync(path.join(gw.dir, 'groundwork.yaml'), 'utf8')
  expect(cfg).toContain('binary: tofu')
  expect(cfg).not.toContain('ansible')
  expect(existsSync(path.join(gw.dir, 'ansible'))).toBe(false)
})

test('app repo: detect screen lists findings, honours exclusions, writes config', async ({ page }) => {
  gw = await start('embedded')
  await page.goto(gw.url)
  await expect(page).toHaveURL(/\/setup\/detect$/)
  await expect(page.getByRole('heading', { name: 'Found infrastructure code in this repo' })).toBeVisible()
  await expect(page.getByText('deploy/terraform/', { exact: true })).toBeVisible()
  await expect(page.getByText('ops/ansible/', { exact: true })).toBeVisible()
  await expect(page.locator('pre.yaml')).toContainText('path: deploy/terraform')
  await expect(page.locator('pre.yaml')).toContainText('approval: required')
  await page.screenshot({ path: path.join(SHOTS, 'detect.png'), fullPage: true })

  await page.getByLabel('Manage ops/ansible').uncheck()
  await expect(page.locator('pre.yaml')).not.toContainText('ops/ansible')
  await page.getByLabel('Require approval before applying to prod').uncheck()
  await expect(page.locator('pre.yaml')).toContainText('approval: none')
  await page.getByLabel('Manage ops/ansible').check()
  await page.getByLabel(/\.tflint\.hcl in deploy\/terraform/).check()

  await page.getByRole('button', { name: 'Start managing' }).click()
  await expect(page.getByRole('region', { name: 'Checks' })).toBeVisible()
  const cfg = readFileSync(path.join(gw.dir, 'groundwork.yaml'), 'utf8')
  expect(cfg).toContain('mode: embedded')
  expect(cfg).toContain('approval: none')
  expect(existsSync(path.join(gw.dir, 'deploy/terraform/.tflint.hcl'))).toBe(true)
  // embedded: nothing else scaffolded
  expect(existsSync(path.join(gw.dir, 'terraform'))).toBe(false)

  // configured repos never show setup again
  await page.goto(gw.base + '/setup/detect')
  await expect(page).toHaveURL(/\/$/)
})
