import { describe, expect, it } from 'vitest'
import type { RunBrief, RunStage } from '../api/types'
import {
  ago,
  cleanError,
  duration,
  finishLine,
  heldFor,
  kindLabel,
  lockOperation,
  parseGoTime,
  parseLockInfo,
  runPhase,
  sampleSeconds,
  similarRuns,
  since,
  typicalRange,
} from './runs'

const T0 = Date.parse('2026-10-11T12:00:00Z')
const at = (s: number) => new Date(T0 + s * 1000).toISOString()

describe('duration', () => {
  it('formats seconds, minutes and hours and never goes negative', () => {
    expect(duration(undefined)).toBe('')
    expect(duration(at(0), at(5))).toBe('5s')
    expect(duration(at(0), at(125))).toBe('2m 5s')
    expect(duration(at(0), at(3725))).toBe('1h 2m')
    expect(duration(at(0), undefined, T0 + 42_000)).toBe('42s') // still running: measured to now
    expect(duration(at(10), at(0))).toBe('0s')
  })
})

describe('ago', () => {
  it('rounds to the largest unit', () => {
    expect(ago(at(-3), T0)).toBe('now')
    expect(ago(at(-45), T0)).toBe('45s')
    expect(ago(at(-90), T0)).toBe('1m')
    expect(ago(at(-7200), T0)).toBe('2h')
    expect(ago(at(-200_000), T0)).toBe('2d')
  })
})

describe('since', () => {
  it('reads as a phrase, never "now ago"', () => {
    expect(since(at(-3), T0)).toBe('just now')
    expect(since(at(-45), T0)).toBe('45s ago')
    expect(since(at(-7200), T0)).toBe('2h ago')
  })
})

describe('kindLabel', () => {
  it('names the command a run stands for', () => {
    expect(kindLabel({ kind: 'tf.init', stage: '', status: 'succeeded' })).toBe('terraform init')
    expect(kindLabel({ kind: 'tf.drift', stage: '', status: 'succeeded' })).toBe('terraform plan -refresh-only')
    expect(kindLabel({ kind: 'tf.plan', stage: 'plan', status: 'running' })).toBe('terraform plan')
    expect(kindLabel({ kind: 'tf.plan', stage: 'approve', status: 'waiting_approval' })).toBe('terraform plan')
    expect(kindLabel({ kind: 'tf.plan', stage: 'apply', status: 'running' })).toBe('terraform apply')
    expect(kindLabel({ kind: 'tf.plan', stage: 'approve', status: 'succeeded' })).toBe('terraform apply')
    expect(kindLabel({ kind: 'ans.playbook', stage: '', status: 'running', target: { playbook: 'site.yml' } as any })).toBe('ansible-playbook site.yml')
    expect(kindLabel({ kind: 'ans.check', stage: '', status: 'running', target: { playbook: 'site.yml' } as any })).toBe('ansible-playbook site.yml --check')
    expect(kindLabel({ kind: 'ans.adhoc', stage: '', status: 'running', target: { module: 'shell', args: 'uptime' } as any })).toBe('ansible -m shell uptime')
  })
})

describe('finishLine', () => {
  it('says how a run ended, when, and how long it took', () => {
    expect(finishLine({ status: 'running', created_at: at(0), started_at: at(0) })).toBe('')
    expect(finishLine({ status: 'failed', created_at: at(0), started_at: at(0), ended_at: at(161) })).toMatch(/^failed \S+ after 2m 41s$/)
    expect(finishLine({ status: 'succeeded', created_at: at(0), ended_at: at(5) })).toMatch(/^finished \S+ after 5s$/)
    expect(finishLine({ status: 'cancelled', created_at: at(0), started_at: at(2), ended_at: at(5) })).toMatch(/^cancelled .* after 3s$/)
  })
})

const st = (name: string, status: RunStage['status'], from?: number, to?: number): RunStage => ({
  name,
  status,
  started_at: from == null ? undefined : at(from),
  ended_at: to == null ? undefined : at(to),
})
// a terraform plan + apply: 30s of planning, 10 minutes waiting for approval, 2 minutes applying
const applied = [
  st('init', 'succeeded', 0, 5),
  st('validate', 'succeeded', 5, 7),
  st('plan', 'succeeded', 7, 30),
  st('summary', 'succeeded', 30, 30),
  st('approve', 'succeeded', 30, 630),
  st('apply', 'succeeded', 630, 750),
]

describe('runPhase and sampleSeconds', () => {
  it('splits a run at its approval and leaves the wait out', () => {
    expect(runPhase(applied)).toBe('after')
    expect(runPhase([st('init', 'running', 0), st('approve', 'pending')])).toBe('before')
    expect(runPhase([st('init', 'running', 0)])).toBe('all')
    expect(sampleSeconds(applied, 'before')).toBe(30)
    expect(sampleSeconds(applied, 'after')).toBe(150)
  })
  it('only counts runs that got through the phase', () => {
    const discarded = [...applied.slice(0, 4), st('approve', 'cancelled', 30, 40), st('apply', 'skipped')]
    expect(sampleSeconds(discarded, 'before')).toBe(30)
    expect(sampleSeconds(discarded, 'after')).toBeNull()
    const failed = [st('init', 'succeeded', 0, 5), st('plan', 'failed', 5, 9), st('approve', 'skipped'), st('apply', 'skipped')]
    expect(sampleSeconds(failed, 'before')).toBeNull()
    expect(sampleSeconds([st('init', 'succeeded', 0, 12)], 'all')).toBe(12)
    expect(sampleSeconds([st('init', 'succeeded')], 'all')).toBeNull() // no timings
  })
})

describe('similarRuns', () => {
  const run = (id: number, over: Partial<RunBrief> = {}): RunBrief =>
    ({
      id,
      kind: 'tf.plan',
      status: 'succeeded',
      stage: 'apply',
      created_at: at(id),
      target: { root: 'envs/prod', env: 'prod', approval_required: true },
      no_changes: false,
      error: '',
      ...over,
    }) as RunBrief
  it('matches the same kind on the same root and env, newest first', () => {
    const me = run(10, { status: 'running' })
    const list = [
      run(1),
      run(9),
      run(5, { kind: 'tf.drift' }),
      run(6, { target: { root: 'envs/dev', env: 'dev', approval_required: false } }),
      run(7, { status: 'running' }),
      me,
      run(8, { status: 'failed' }),
    ]
    expect(similarRuns(list, me).map((r) => r.id)).toEqual([9, 8, 1])
    expect(similarRuns(list, me, 1).map((r) => r.id)).toEqual([9])
  })
})

describe('typicalRange', () => {
  it('needs two samples', () => {
    expect(typicalRange([])).toBe('')
    expect(typicalRange([150])).toBe('')
  })
  it('reads in seconds, minutes or hours', () => {
    expect(typicalRange([22, 41])).toBe('usually 20–40s')
    expect(typicalRange([30, 31])).toBe('usually about 30s')
    expect(typicalRange([130, 170])).toBe('usually 2–3 min')
    expect(typicalRange([180, 185])).toBe('usually about 3 min')
    expect(typicalRange([2 * 3600, 3 * 3600])).toBe('usually 2–3 h')
  })
  it('drops the fastest and slowest of four or more', () => {
    expect(typicalRange([5, 130, 160, 3000])).toBe('usually 2–3 min')
  })
})

const LOCK = `plan failed: exit status 1: Error acquiring the state lock: Error message: resource temporarily unavailable
Lock Info:
  ID:        eb4ca245-50b1-5658-c0c3-a4c69baa2888
  Path:      terraform.tfstate
  Operation: OperationTypeApply
  Who:       ci@runner-01
  Version:   1.16.5
  Created:   2026-10-07 16:27:41.671512 +0000 UTC
  Info:

Terraform acquires a state lock to protect the state from being written`

describe('state lock errors', () => {
  it('reads the lock info out of the error', () => {
    const l = parseLockInfo(LOCK)!
    expect(l).toMatchObject({
      id: 'eb4ca245-50b1-5658-c0c3-a4c69baa2888',
      path: 'terraform.tfstate',
      operation: 'OperationTypeApply',
      who: 'ci@runner-01',
      version: '1.16.5',
    })
    expect(l.created).toBe(Date.parse('2026-10-07T16:27:41.671Z'))
    expect(lockOperation(l.operation)).toBe('apply')
    expect(parseLockInfo('Error: Invalid reference')).toBeNull()
  })
  it('parses Go times with and without an offset', () => {
    expect(parseGoTime('2026-10-21 11:21:07 UTC')).toBe(Date.parse('2026-10-21T11:21:07Z'))
    expect(parseGoTime('2026-10-21 14:21:07 +0300 EAT')).toBe(Date.parse('2026-10-21T11:21:07Z'))
    expect(parseGoTime('yesterday')).toBeUndefined()
  })
  it('says how long a lock has been held', () => {
    expect(heldFor(T0 - 20_000, T0)).toBe('under a minute')
    expect(heldFor(T0 - 9 * 60_000, T0)).toBe('9 min')
    expect(heldFor(T0 - 3 * 3600_000, T0)).toBe('3 h')
    expect(heldFor(T0 - 3 * 86400_000, T0)).toBe('3 d')
  })
})

describe('cleanError', () => {
  it('keeps the first line and drops exit codes and repeated prefixes', () => {
    expect(cleanError(LOCK)).toBe('plan failed: Error acquiring the state lock: resource temporarily unavailable')
    expect(cleanError('\n╷\n│ Error: Invalid reference\n│ \n│   on main.tf line 3')).toBe('Invalid reference')
    expect(cleanError('\x1b[31mError: \x1b[0mInvalid count')).toBe('Invalid count')
    expect(cleanError('interrupted: groundwork stopped')).toBe('interrupted: groundwork stopped')
  })
})
