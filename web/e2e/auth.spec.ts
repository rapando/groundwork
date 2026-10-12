import { test, expect, start, Instance, PASSWORD } from './helpers'
import { readFileSync } from 'node:fs'
import path from 'node:path'

let gw: Instance
test.afterEach(async () => {
  await gw?.stop()
})

test('first visit sets a password up; sign out and back in', async ({ page, context }) => {
  gw = await start('iac-only', { init: true, signIn: false })
  await page.goto(gw.url)
  await expect(page.getByRole('heading', { name: 'Set up sign-in' })).toBeVisible()
  await expect(page.locator('meta[name="groundwork-token"]')).toHaveCount(0)

  await page.getByLabel('Choose a password').fill('short')
  await page.getByLabel('Confirm it').fill('short')
  await page.getByRole('button', { name: 'Set password' }).click()
  await expect(page.getByRole('alert')).toContainText('at least 8')

  await page.getByLabel('Choose a password').fill(PASSWORD)
  await page.getByLabel('Confirm it').fill(PASSWORD)
  await page.getByRole('button', { name: 'Set password' }).click()
  await expect(page.getByRole('navigation', { name: 'Primary' })).toBeVisible()
  const auth = readFileSync(path.join(gw.home, 'auth.json'), 'utf8')
  expect(auth).not.toContain(PASSWORD)

  // a reload stays signed in; the cookie is HttpOnly and strict
  await page.reload()
  await expect(page.getByRole('navigation', { name: 'Primary' })).toBeVisible()
  const ck = (await context.cookies()).find((c) => c.name === 'groundwork_session')
  expect(ck?.httpOnly).toBe(true)
  expect(ck?.sameSite).toBe('Strict')

  // sign out from the project list
  await page.goto(new URL(gw.url).origin + '/')
  await page.getByRole('button', { name: 'Sign-in' }).click()
  const dlg = page.getByRole('dialog', { name: 'Sign-in' })
  await expect(dlg.getByText('set', { exact: true })).toBeVisible()
  await dlg.getByRole('button', { name: 'Sign out', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()

  await page.getByLabel('Password').fill('not the password')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('alert')).toContainText('wrong password')
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible()
})

test('127.0.0.1 redirects to localhost, where the session lives', async ({ page }) => {
  gw = await start('iac-only', { init: true })
  const u = new URL(gw.url)
  await page.goto(`http://127.0.0.1:${u.port}${u.pathname}`)
  await expect(page).toHaveURL(new RegExp(`^http://localhost:${u.port}/`))
  await expect(page.getByRole('navigation', { name: 'Primary' })).toBeVisible()
})

// Chrome's virtual authenticator stands in for Touch ID: it holds a
// resident key and always verifies the user.
test('a passkey sets sign-in up and signs back in', async ({ page, context }) => {
  gw = await start('iac-only', { init: true, signIn: false })
  const cdp = await context.newCDPSession(page)
  await cdp.send('WebAuthn.enable')
  await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true },
  })

  await page.goto(gw.url)
  await page.getByRole('button', { name: 'Use Touch ID or a passkey' }).click()
  await expect(page.getByRole('navigation', { name: 'Primary' })).toBeVisible()

  await context.clearCookies()
  await page.goto(gw.url)
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  // passkey only: no password field
  await expect(page.getByLabel('Password')).toHaveCount(0)
  await page.getByRole('button', { name: 'Sign in with a passkey' }).click()
  await expect(page.getByRole('navigation', { name: 'Primary' })).toBeVisible()

  // the passkey is listed, and as the only method it can't be removed
  await page.goto(new URL(gw.url).origin + '/')
  await page.getByRole('button', { name: 'Sign-in' }).click()
  const dlg = page.getByRole('dialog', { name: 'Sign-in' })
  await expect(dlg.getByRole('list', { name: 'Passkeys' }).getByRole('listitem')).toHaveCount(1)
  await expect(dlg.getByRole('button', { name: /^Remove / })).toBeDisabled()
  // a password as well; then the passkey can go
  await dlg.getByRole('button', { name: 'Set a password' }).click()
  await dlg.getByLabel('New password').fill(PASSWORD)
  await dlg.getByRole('button', { name: 'Save password' }).click()
  await expect(dlg.getByText('Password saved.')).toBeVisible()
  await expect(dlg.getByRole('button', { name: /^Remove This/ })).toBeEnabled()
})
