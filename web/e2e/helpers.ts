import { spawn, ChildProcess, execFileSync } from 'node:child_process'
import { cpSync, mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import net from 'node:net'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))

export const BIN = process.env.GW_BIN ?? path.resolve(here, '../../bin/groundwork')
export const FIXTURES = path.resolve(here, '../../testdata/repos')

export interface Instance { url: string; dir: string; output: () => string; stop: () => Promise<void>; kill: () => void; restart: (env?: Record<string, string>) => Promise<Instance> }

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
export interface StartOpts { tips?: boolean; git?: boolean; init?: boolean; commit?: boolean; files?: Record<string, string>; env?: Record<string, string>; dir?: string }

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
  const env = { ...process.env, ...(opts.tips ? {} : { GROUNDWORK_TIPS: 'off' }), ...(opts.env ?? {}), PATH: `${path.join(here, 'fakebin')}${path.delimiter}${process.env.PATH}` }
  if (!reuse && opts.git !== false) execFileSync('git', ['init', '-q'], { cwd: dir })
  if (!reuse && opts.init) execFileSync(BIN, ['init', '--detect', '--yes'], { cwd: dir, env })
  if (!reuse && opts.commit) {
    const g = (...a: string[]) => execFileSync('git', ['-c', 'user.name=t', '-c', 'user.email=t@e.com', ...a], { cwd: dir })
    g('add', '-A'); g('commit', '-qm', 'initial')
  }
  const port = await freePort()
  const child: ChildProcess = spawn(BIN, ['--no-open', '--port', String(port)], { cwd: dir, env })
  let all = '' // everything the server printed, for leak checks
  child.stderr!.on('data', (d) => { all += d })
  const url = await new Promise<string>((resolve, reject) => {
    let buf = ''
    child.stdout!.on('data', (d) => {
      all += d
      buf += d
      const m = buf.match(/http:\/\/127\.0\.0\.1:\d+\/\?t=[0-9a-f]+/)
      if (m) resolve(m[0])
    })
    child.on('exit', (c) => reject(new Error('groundwork exited ' + c + ' ' + buf)))
    setTimeout(() => reject(new Error('timeout waiting for URL: ' + buf)), 10_000)
  })
  // a child killed by a signal has exitCode null but a signalCode, so check both
  const stop = () => new Promise<void>((r) => { if (child.exitCode !== null || child.signalCode !== null) return r(); child.once('exit', () => r()); child.kill('SIGINT') })
  return {
    url, dir, stop, output: () => all,
    kill: () => { child.kill('SIGKILL') }, // simulates a crash: no graceful shutdown
    restart: async (e) => { await stop().catch(() => {}); return start(undefined, { ...opts, dir, env: { ...(opts.env ?? {}), ...(e ?? {}) } }) },
  }
}
