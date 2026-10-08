import { test, expect } from '@playwright/test'
import { existsSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { start, Instance } from './helpers'

const SHOTS = path.resolve(path.dirname(fileURLToPath(import.meta.url)), 'screens')
let gw: Instance
test.afterEach(async () => { await gw?.stop() })

test('requests without the session token are rejected', async ({ request }) => {
  gw = await start('embedded')
  const origin = new URL(gw.url).origin
  const api = origin + '/api' + new URL(gw.base).pathname
  expect((await request.get(api + '/workspace')).status()).toBe(401)
  expect((await request.get(api + '/events')).status()).toBe(401)
  expect((await request.get(origin + '/api/projects')).status()).toBe(401)
  expect((await request.get(gw.base + '/')).status()).toBe(401)
  expect((await request.get(origin + '/')).status()).toBe(401)
})

test('empty repo: scaffold screen validates, previews, creates', async ({ page }) => {
  gw = await start()
  await page.goto(gw.url)
  await expect(page).toHaveURL(/\/setup\/scaffold$/)
  await expect(page.getByRole('heading', { name: 'Scaffold an IaC repo' })).toBeVisible()

  // region is required for AWS: the preview is withheld and the field shows why
  await expect(page.getByText('enter an AWS region')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Create files' })).toBeDisabled()

  await page.getByPlaceholder('e.g. eu-west-1').fill('eu-west-1')
  await expect(page.getByText(/^\+\d+ files/)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Create files' })).toBeEnabled()

  // preview shows file contents and never writes
  await page.getByRole('button', { name: 'main.tf', exact: true }).first().click()
  await expect(page.locator('.viewer pre')).toContainText('terraform')
  expect(existsSync(path.join(gw.dir, 'groundwork.yaml'))).toBe(false)
  await page.screenshot({ path: path.join(SHOTS, 'scaffold.png'), fullPage: true })

  // switch layout → preview follows
  await page.getByText('Workspaces', { exact: true }).click()
  await expect(page.getByRole('button', { name: 'dev.tfvars' })).toBeVisible()

  await page.getByRole('button', { name: 'Create files' }).click()
  await expect(page.getByRole('heading', { name: 'Repository scaffolded' })).toBeVisible()
  expect(readFileSync(path.join(gw.dir, 'groundwork.yaml'), 'utf8')).toContain('workspace: prod')
  expect(existsSync(path.join(gw.dir, 'terraform/modules/network/main.tf'))).toBe(true)

  await page.getByRole('button', { name: 'Open console' }).click()
  await expect(page.getByRole('region', { name: 'Verification' })).toBeVisible()
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
  await expect(page.getByRole('region', { name: 'Verification' })).toBeVisible()
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
