import { test, expect, Page, start, Instance } from './helpers'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const SHOTS = path.resolve(path.dirname(fileURLToPath(import.meta.url)), 'screens')
let gw: Instance
test.afterEach(async () => {
  await gw?.stop()
})

async function open(page: Page) {
  await page.goto(gw.url)
  await expect(page.getByRole('link', { name: 'Overview', exact: true })).toBeVisible()
  await page.getByRole('link', { name: 'Variables', exact: true }).click()
}

test('terraform matrix: precedence, edit a value, reveal a sensitive one', async ({ page }) => {
  gw = await start('vars-lab', { init: true })
  await open(page)
  const matrix = page.getByRole('region', { name: 'Variables by environment' })
  const row = (name: string) => matrix.getByRole('row').filter({ has: page.getByRole('rowheader', { name: new RegExp('^' + name) }) })
  await expect(matrix.getByRole('columnheader', { name: 'dev' })).toBeVisible()
  await expect(row('replicas').getByRole('cell').nth(0)).toContainText('3override.auto.tfvars') // auto.tfvars beats terraform.tfvars
  await expect(row('replicas').getByRole('cell').nth(1)).toContainText('6terraform.tfvars')
  await expect(row('db_password')).toContainText('••')
  await expect(row('db_password')).not.toContainText('s3cret')
  await expect(row('unused_flag')).toContainText('unused')

  // set region for dev in its terraform.tfvars
  await row('region').getByRole('button', { name: 'region', exact: true }).click()
  const detail = page.getByRole('complementary', { name: 'Variable detail' })
  await expect(detail.getByText('required')).toBeVisible()
  await detail.getByLabel('Value for dev').fill('"eu-central-1"')
  await detail.getByRole('button', { name: 'Preview change' }).click()
  await expect(detail.getByRole('region', { name: 'Diff' })).toContainText('+region')
  await detail.getByRole('button', { name: 'Save and validate' }).click()
  await expect(detail.getByText(/Saved to envs\/dev\/terraform.tfvars/)).toBeVisible()
  await expect(row('region').getByRole('cell').nth(0)).toContainText('"eu-central-1"')
  expect(readFileSync(path.join(gw.dir, 'envs/dev/terraform.tfvars'), 'utf8')).toContain('region')

  // a bad literal is explained, not written
  await row('replicas').getByRole('cell').nth(1).click()
  await detail.getByLabel('Value for prod').fill('three')
  await detail.getByRole('button', { name: 'Preview change' }).click()
  await expect(detail.getByRole('alert')).toContainText('strings need quotes')

  // sensitive: can't be edited here; reveal shows it once
  await row('db_password').getByRole('button', { name: 'db_password', exact: true }).click()
  await expect(detail.getByText(/won't write it to a tfvars file/)).toBeVisible()
  await detail.getByRole('button', { name: 'Reveal' }).click()
  await expect(detail.getByText('"s3cret-dev-pw"')).toBeVisible()
  await page.screenshot({ path: path.join(SHOTS, 'variables.png'), fullPage: true })
  await detail.getByRole('button', { name: 'Hide' }).click()
  await expect(detail.getByText('"s3cret-dev-pw"')).toHaveCount(0)

  // the plaintext scan flags both committed passwords; secrets tab shows the audit
  const secrets = page.getByRole('region', { name: 'Secrets' })
  await expect(secrets.locator('.finding')).toHaveCount(2)
  // a finding opens its variable's row, in that file's environment
  await row('replicas').getByRole('cell').nth(0).click()
  await secrets.locator('.finding', { hasText: 'envs/prod/' }).getByRole('button', { name: 'Show db_password' }).click()
  await expect(row('db_password')).toHaveClass(/sel/)
  await expect(row('db_password').getByRole('cell').nth(1)).toHaveClass(/cur/) // the prod column
  await page.getByRole('button', { name: /^Secrets/ }).click()
  await expect(secrets.getByText('Recent reveals')).toBeVisible()
  await expect(secrets.getByText(/db_password · envs\/dev/)).toBeVisible()
  expect(gw.output()).not.toContain('s3cret-dev-pw')
})

const CONFIG = `version: 1
mode: standalone
ansible:
  projects:
    - path: .
      config: ansible.cfg
      vault_password_file: .vault_pass
      inventories:
        dev: inventory/dev.yml
checks:
  enabled: [yamllint, secrets]
  on_save: true
`

test('ansible: group vars matrix, move a plaintext secret to the vault, reveal it', async ({ page }) => {
  const orig = readFileSync(path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../testdata/repos/ansible-lab/inventory/group_vars/all.yml'), 'utf8')
  gw = await start('ansible-lab', {
    files: {
      'groundwork.yaml': CONFIG,
      '.vault_pass': 'e2e-vault-pw\n',
      'inventory/group_vars/all.yml': orig + 'smtp_password: Mail-Pw-9931x\n',
    },
  })
  await open(page)
  await page.getByRole('button', { name: /^Ansible/ }).click()
  const matrix = page.getByRole('region', { name: 'Group variables by environment' })
  await expect(matrix.getByRole('rowheader', { name: 'web · nginx_workers' })).toBeVisible()
  await expect(matrix.getByRole('rowheader', { name: 'smtp_password' })).toBeVisible()
  await expect(matrix).not.toContainText('Mail-Pw')

  await page.getByRole('button', { name: /^Secrets/ }).click()
  const secrets = page.getByRole('region', { name: 'Secrets' })
  const finding = secrets.locator('.finding', { hasText: 'smtp_password' })
  await finding.getByRole('button', { name: 'Move to vault' }).click()
  const dlg = page.getByRole('dialog', { name: 'Move to ansible-vault' })
  await expect(dlg.getByRole('region', { name: 'Diff' })).toContainText('smtp_password: !vault |')
  await expect(dlg).not.toContainText('Mail-Pw')
  await dlg.getByRole('button', { name: 'Encrypt' }).click()
  await expect(dlg).toHaveCount(0)
  await expect(secrets.getByText(/rotate it/)).toBeVisible()
  await expect(finding).toHaveCount(0)
  const text = readFileSync(path.join(gw.dir, 'inventory/group_vars/all.yml'), 'utf8')
  expect(text).toContain('smtp_password: !vault |')
  expect(text).not.toContain('Mail-Pw')

  const srow = secrets.locator('.srow', { hasText: 'smtp_password' })
  await expect(srow.getByText('can decrypt')).toBeVisible({ timeout: 15_000 })
  await srow.getByRole('button', { name: 'Reveal' }).click()
  await expect(srow.getByText('Mail-Pw-9931x')).toBeVisible()
  expect(gw.output()).not.toContain('Mail-Pw-9931x')
})

test('ansible-vault: add, edit, create, open from Code, and rekey', async ({ page }) => {
  gw = await start('ansible-lab', { files: { 'groundwork.yaml': CONFIG, '.vault_pass': 'e2e-vault-pw\n' } })
  await open(page)
  await page.getByRole('button', { name: /^Secrets/ }).click()
  const secrets = page.getByRole('region', { name: 'Secrets' })
  const srow = (name: string) => secrets.locator('.srow', { hasText: name })
  const vars = () => readFileSync(path.join(gw.dir, 'inventory/group_vars/all.yml'), 'utf8')

  // add an encrypted variable to an existing vars file
  await secrets.getByRole('button', { name: 'New secret' }).click()
  let dlg = page.getByRole('dialog', { name: 'New secret' })
  await dlg.getByLabel('File, from the repository root').fill('inventory/group_vars/all.yml')
  await dlg.getByLabel('Variable name').fill('api_token')
  await dlg.getByLabel('Value').fill('Tok-First-1')
  await dlg.getByRole('button', { name: 'Encrypt and save' }).click()
  await expect(dlg).toHaveCount(0)
  await expect(secrets.getByText('Added api_token to inventory/group_vars/all.yml, encrypted.')).toBeVisible()
  expect(vars()).toContain('api_token: !vault |')
  expect(vars()).not.toContain('Tok-First')

  // edit it in place
  await expect(srow('api_token').getByText('can decrypt')).toBeVisible({ timeout: 15_000 })
  await srow('api_token').getByRole('button', { name: 'Edit' }).click()
  dlg = page.getByRole('dialog', { name: 'Edit api_token' })
  await expect(dlg.getByLabel('Decrypted value')).toHaveValue('Tok-First-1')
  await dlg.getByLabel('Decrypted value').fill('Tok-Second-2')
  await dlg.getByRole('button', { name: 'Save encrypted' }).click()
  await expect(dlg).toHaveCount(0)
  await expect(secrets.getByText(/Saved and re-encrypted/)).toBeVisible()
  await srow('api_token').getByRole('button', { name: 'Reveal' }).click()
  await expect(srow('api_token').getByText('Tok-Second-2')).toBeVisible()

  // a new whole-file vault, then open it from the Code screen
  await secrets.getByRole('button', { name: 'New secret' }).click()
  dlg = page.getByRole('dialog', { name: 'New secret' })
  await dlg.getByRole('button', { name: 'Encrypted file' }).click()
  await dlg.getByLabel('File, from the repository root').fill('inventory/group_vars/dev/vault.yml')
  await dlg.getByLabel('Content').fill('db_password: Db-Pw-3\n')
  await dlg.getByRole('button', { name: 'Encrypt and save' }).click()
  await expect(dlg).toHaveCount(0)
  expect(readFileSync(path.join(gw.dir, 'inventory/group_vars/dev/vault.yml'), 'utf8')).toMatch(/^\$ANSIBLE_VAULT;1\.1;AES256/)
  await page.goto(gw.url.replace(/\/?$/, '/code/inventory/group_vars/dev/vault.yml'))
  await expect(page.getByRole('note')).toContainText('encrypted with ansible-vault')
  await page.getByRole('link', { name: 'Edit decrypted' }).click()
  dlg = page.getByRole('dialog', { name: 'Edit vault.yml' })
  await expect(dlg.getByLabel('Decrypted value')).toHaveValue('db_password: Db-Pw-3\n')
  await dlg.getByRole('button', { name: 'Cancel' }).click()

  // rekey: everything is re-encrypted and the new password written
  await secrets.getByRole('button', { name: 'Rekey…' }).click()
  dlg = page.getByRole('dialog', { name: 'Rekey ansible-vault' })
  await expect(dlg.getByRole('listitem')).toHaveCount(2)
  await dlg.getByLabel('New password', { exact: true }).fill('e2e-vault-pw-2')
  await dlg.getByLabel('Repeat it').fill('e2e-vault-pw-2')
  await expect(dlg.getByLabel(/Write the new password to/)).toBeChecked()
  await dlg.getByRole('button', { name: 'Rekey', exact: true }).click()
  await expect(dlg).toHaveCount(0)
  await expect(secrets.getByText(/Re-encrypted 2 values in 2 files/)).toBeVisible()
  expect(readFileSync(path.join(gw.dir, '.vault_pass'), 'utf8')).toBe('e2e-vault-pw-2\n')
  await expect(srow('api_token').getByText('can decrypt')).toBeVisible({ timeout: 15_000 })
  await srow('api_token').getByRole('button', { name: 'Reveal' }).click()
  await expect(srow('api_token').getByText('Tok-Second-2')).toBeVisible()
  for (const s of ['Tok-First-1', 'Tok-Second-2', 'Db-Pw-3', 'e2e-vault-pw-2']) expect(gw.output()).not.toContain(s)
})

test('ansible-vault: type the vault password instead of a password file', async ({ page }) => {
  gw = await start('ansible-lab', { files: { 'groundwork.yaml': CONFIG.replace('      vault_password_file: .vault_pass\n', '') } })
  await open(page)
  await page.getByRole('button', { name: /^Secrets/ }).click()
  const secrets = page.getByRole('region', { name: 'Secrets' })
  const srow = (name: string) => secrets.locator('.srow', { hasText: name })

  // nothing is encrypted yet: the typed password becomes the vault's
  await expect(secrets.getByText('Vault locked', { exact: true })).toBeVisible()
  await secrets.getByRole('button', { name: 'Unlock…' }).click()
  let dlg = page.getByRole('dialog', { name: 'Unlock ansible-vault' })
  await dlg.getByLabel('Vault password').fill('Typed-Vault-7')
  await dlg.getByRole('button', { name: 'Unlock' }).click()
  await expect(dlg).toHaveCount(0)
  await expect(secrets.getByText(/couldn't be checked/)).toBeVisible()
  await expect(secrets.getByText('Vault unlocked', { exact: true })).toBeVisible()

  await secrets.getByRole('button', { name: 'New secret' }).click()
  dlg = page.getByRole('dialog', { name: 'New secret' })
  await dlg.getByLabel('File, from the repository root').fill('inventory/group_vars/all.yml')
  await dlg.getByLabel('Variable name').fill('api_token')
  await dlg.getByLabel('Value').fill('Tok-Typed-1')
  await dlg.getByRole('button', { name: 'Encrypt and save' }).click()
  await expect(dlg).toHaveCount(0)
  await expect(srow('api_token').getByText('can decrypt')).toBeVisible({ timeout: 15_000 })

  // locked again, a wrong password is refused, the right one opens the value
  await secrets.getByRole('button', { name: 'Lock' }).click()
  await expect(srow('api_token').getByText('locked')).toBeVisible()
  await srow('api_token').getByRole('button', { name: 'Unlock…' }).click()
  dlg = page.getByRole('dialog', { name: 'Unlock ansible-vault' })
  await dlg.getByLabel('Vault password').fill('not-it')
  await dlg.getByRole('button', { name: 'Unlock' }).click()
  await expect(dlg.getByRole('alert')).toContainText("doesn't open this vault")
  await dlg.getByLabel('Vault password').fill('Typed-Vault-7')
  await dlg.getByRole('button', { name: 'Unlock' }).click()
  await expect(dlg).toHaveCount(0)
  await expect(secrets.getByText(/Unlocked\. groundwork keeps the password in memory/)).toBeVisible()
  await expect(srow('api_token').getByText('can decrypt')).toBeVisible({ timeout: 15_000 })
  await srow('api_token').getByRole('button', { name: 'Reveal' }).click()
  await expect(srow('api_token').getByText('Tok-Typed-1')).toBeVisible()
  for (const s of ['Tok-Typed-1', 'Typed-Vault-7']) expect(gw.output()).not.toContain(s)
})
