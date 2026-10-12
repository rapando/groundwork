import { test as base, type BrowserContext } from '@playwright/test'
import { spawn, ChildProcess, execFileSync } from 'node:child_process'
import { cpSync, existsSync, mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import net from 'node:net'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))

// Specs import test and expect from here: the fixture below lets start() sign
// the test's browser in.
export * from '@playwright/test'

let context: BrowserContext | null = null
export const test = base.extend<{ signedIn: void }>({
  signedIn: [
    async ({ context: c }, use) => {
      context = c
      await use()
      context = null
    },
    { auto: true },
  ],
})

/** The password start() sets up (or signs in with) for every instance. */
export const PASSWORD = 'e2e-password'

export const BIN = process.env.GW_BIN ?? path.resolve(here, '../../bin/groundwork')
export const FIXTURES = path.resolve(here, '../../testdata/repos')

/** url opens the project; base is the project's UI root (origin + /p/<id>). */
export interface Instance {
  url: string
  /** the browser session cookie start() got, "" with signIn: false */
  session: string
  base: string
  dir: string
  home: string
  output: () => string
  stop: () => Promise<void>
  kill: () => void
  restart: (env?: Record<string, string>) => Promise<Instance>
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = net.createServer()
    s.listen(0, '127.0.0.1', () => {
      const p = (s.address() as net.AddressInfo).port
      s.close(() => resolve(p))
    })
    s.on('error', reject)
  })
}

/** Starts groundwork in a fresh temp repo (optionally seeded from a fixture). */
export interface StartOpts {
  tips?: boolean
  git?: boolean
  init?: boolean
  commit?: boolean
  files?: Record<string, string>
  env?: Record<string, string>
  dir?: string
  home?: string
  /** false leaves sign-in as the first visit finds it: not set up (or set up but signed out) */
  signIn?: boolean
}

/** Sets a password up on first start (signs in with it afterwards) and returns the session cookie. */
async function signIn(origin: string): Promise<string> {
  const st = await (await fetch(origin + '/auth/status')).json()
  const res = await fetch(origin + (st.configured ? '/auth/password/login' : '/auth/password/setup'), {
    method: 'POST',
    headers: { Origin: origin, 'Content-Type': 'application/json' },
    body: JSON.stringify({ password: PASSWORD }),
  })
  if (!res.ok) throw new Error('sign-in failed: ' + res.status + ' ' + (await res.text()))
  const m = res.headers
    .getSetCookie()
    .join('\n')
    .match(/groundwork_session=([^;]+)/)
  if (!m) throw new Error('no session cookie')
  await context?.addCookies([{ name: 'groundwork_session', value: m[1], url: origin }])
  return m[1]
}

export async function start(fixture?: string, opts: StartOpts = {}): Promise<Instance> {
  const reuse = !!opts.dir
  const dir = opts.dir ?? mkdtempSync(path.join(tmpdir(), 'gw-e2e-'))
  if (!reuse) {
    if (fixture) cpSync(path.join(FIXTURES, fixture), dir, { recursive: true })
    else mkdirSync(dir, { recursive: true })
  }
  for (const [rel, text] of reuse ? [] : Object.entries(opts.files ?? {})) {
    mkdirSync(path.dirname(path.join(dir, rel)), { recursive: true })
    writeFileSync(path.join(dir, rel), text)
  }
  // each instance is its own service with its own project registry
  const home = opts.home ?? mkdtempSync(path.join(tmpdir(), 'gw-e2e-home-'))
  const env = {
    ...process.env,
    GROUNDWORK_HOME: home,
    ...(opts.tips ? {} : { GROUNDWORK_TIPS: 'off' }),
    ...(opts.env ?? {}),
    PATH: `${path.join(here, 'fakebin')}${path.delimiter}${process.env.PATH}`,
  }
  if (!reuse && opts.git !== false) execFileSync('git', ['init', '-q'], { cwd: dir })
  if (!reuse && opts.init) execFileSync(BIN, ['init', '--detect', '--yes'], { cwd: dir, env })
  if (!reuse && opts.commit) {
    const g = (...a: string[]) => execFileSync('git', ['-c', 'user.name=t', '-c', 'user.email=t@e.com', ...a], { cwd: dir })
    g('add', '-A')
    g('commit', '-qm', 'initial')
  }
  const port = await freePort()
  // a restart after the repo folder went missing (to test "unavailable"): serve without importing it
  const haveDir = existsSync(dir)
  const args = ['serve', '--no-open', '--port', String(port), ...(haveDir ? ['.'] : [])]
  const child: ChildProcess = spawn(BIN, args, { cwd: haveDir ? dir : home, env })
  let all = '' // everything the server printed, for leak checks
  child.stderr!.on('data', (d) => {
    all += d
  })
  const url = await new Promise<string>((resolve, reject) => {
    let buf = ''
    child.stdout!.on('data', (d) => {
      all += d
      buf += d
      const m = buf.match(haveDir ? /http:\/\/localhost:\d+\/p\/[a-z0-9-]+\// : /http:\/\/localhost:\d+\//)
      if (m) resolve(m[0])
    })
    child.on('exit', (c) => reject(new Error('groundwork exited ' + c + ' ' + buf)))
    setTimeout(() => reject(new Error('timeout waiting for URL: ' + buf)), 10_000)
  })
  // a child killed by a signal has exitCode null but a signalCode, so check both
  const stop = () =>
    new Promise<void>((r) => {
      if (child.exitCode !== null || child.signalCode !== null) return r()
      child.once('exit', () => r())
      child.kill('SIGINT')
    })
  const session = opts.signIn === false ? '' : await signIn(new URL(url).origin)
  return {
    url,
    session,
    base: new URL(url).origin + new URL(url).pathname.replace(/\/$/, ''),
    dir,
    home,
    stop,
    output: () => all,
    kill: () => {
      child.kill('SIGKILL')
    }, // simulates a crash: no graceful shutdown
    restart: async (e) => {
      await stop().catch(() => {})
      return start(undefined, { ...opts, dir, home, env: { ...(opts.env ?? {}), ...(e ?? {}) } })
    },
  }
}
