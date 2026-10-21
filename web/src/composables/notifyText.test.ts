import { describe, expect, it } from 'vitest'
import type { Notification } from '../api/types'
import { clock, dotToken, groupByDay, notificationPath, outranks, runOnPath, shortLabel, tabTitle, watching } from './notifyText'

const n = (o: Partial<Notification>): Notification => ({
  id: 1,
  event: 'failed',
  run_id: 14,
  kind: 'tf.plan',
  stage: 'apply',
  label: 'terraform apply',
  root: 'terraform/envs/prod',
  env: 'prod',
  title: 'Apply #14 failed on prod',
  text: '',
  duration_s: 161,
  at: '2026-10-21T14:30:00Z',
  toast: true,
  desktop: true,
  ...o,
})

describe('tabTitle', () => {
  it('names what happened and to which run', () => {
    expect(tabTitle(n({ kind: 'ans.playbook', stage: '', label: 'ansible-playbook site.yml', env: 'prod' }))).toBe('✕ Failed · site.yml · groundwork')
    expect(tabTitle(n({ event: 'succeeded' }))).toBe('✓ Applied · apply #14 on prod · groundwork')
    expect(tabTitle(n({ event: 'succeeded', kind: 'ans.playbook', stage: '', label: 'ansible-playbook deploy.yaml' }))).toBe(
      '✓ Succeeded · deploy.yaml · groundwork',
    )
    expect(tabTitle(n({ event: 'approval', stage: 'approve', label: 'terraform plan', run_id: 15 }))).toBe('● Needs approval · plan #15 on prod · groundwork')
    expect(tabTitle(n({ event: 'drift', kind: 'tf.drift' }))).toBe('● Drift · prod · groundwork')
  })
  it('falls back to "run" for an empty label', () => {
    expect(shortLabel(n({ label: '', env: '' }))).toBe('run #14')
  })
})

describe('dotToken', () => {
  it('uses the status colours', () => {
    expect(dotToken('failed')).toBe('--fail')
    expect(dotToken('approval')).toBe('--warn')
    expect(dotToken('drift')).toBe('--warn')
    expect(dotToken('succeeded')).toBe('--ok')
  })
})

describe('notificationPath', () => {
  it('opens the run, its plan for approvals, the drifted graph for drift', () => {
    expect(notificationPath(n({}))).toBe('/runs/14')
    expect(notificationPath(n({ event: 'approval' }))).toBe('/runs/14/plan')
    expect(notificationPath(n({ event: 'drift' }))).toBe('/graph?root=terraform%2Fenvs%2Fprod&env=prod&filter=drifted')
  })
})

describe('runOnPath and watching', () => {
  it('finds the run a page shows', () => {
    expect(runOnPath('/runs/14')).toBe(14)
    expect(runOnPath('/runs/14/plan')).toBe(14)
    expect(runOnPath('/runs')).toBe(0)
    expect(runOnPath('/code/runs/14')).toBe(0)
  })
  it('counts only a visible tab on that run as watching', () => {
    expect(watching(14, false, 14)).toBe(true)
    expect(watching(14, true, 14)).toBe(false)
    expect(watching(14, false, 13)).toBe(false)
  })
})

describe('outranks', () => {
  it('keeps a failure in the title over a later success', () => {
    expect(outranks('succeeded', null)).toBe(true)
    expect(outranks('succeeded', 'failed')).toBe(false)
    expect(outranks('failed', 'approval')).toBe(true)
    expect(outranks('failed', 'failed')).toBe(true)
  })
})

describe('groupByDay', () => {
  it('groups a newest-first list under Today, Yesterday and dates', () => {
    const now = new Date(2026, 9, 21, 15, 0).getTime()
    const at = (d: number, h: number) => new Date(2026, 9, d, h, 5).toISOString()
    const g = groupByDay([{ at: at(21, 14) }, { at: at(21, 9) }, { at: at(20, 23) }, { at: at(18, 10) }, { at: at(18, 8) }], now)
    expect(g.map((x) => [x.day, x.items.length])).toEqual([
      ['Today', 2],
      ['Yesterday', 1],
      [new Date(2026, 9, 18).toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short' }), 2],
    ])
    expect(groupByDay([], now)).toEqual([])
  })
  it('formats local clock times', () => {
    expect(clock(new Date(2026, 9, 21, 9, 5).toISOString())).toBe('09:05')
  })
})
