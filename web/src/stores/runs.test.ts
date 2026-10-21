import { describe, expect, it } from 'vitest'
import { ago, duration, kindLabel, since } from './runs'

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
