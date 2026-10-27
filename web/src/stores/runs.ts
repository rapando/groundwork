import { defineStore } from 'pinia'
import { api } from '../api/client'
import type { EnvView, RunBrief, RunStage } from '../api/types'
import { onEvent } from '../composables/events'
import { swallow } from '../api/swallow'

let timer: number | undefined

export const useRuns = defineStore('runs', {
  state: () => ({ list: [] as RunBrief[], envs: [] as EnvView[], loaded: false }),
  getters: {
    live: (s) => s.list.filter((r) => r.status === 'running' || r.status === 'queued').length,
    awaiting: (s) => s.list.filter((r) => r.status === 'waiting_approval').length,
  },
  actions: {
    async refresh() {
      const [runs, envs] = await Promise.all([api<{ runs: RunBrief[] }>('/runs?limit=100'), api<{ envs: EnvView[] }>('/envs')])
      this.list = runs.runs
      this.envs = envs.envs
      this.loaded = true
    },
    async submit(body: Record<string, unknown>): Promise<RunBrief> {
      const r = await api<{ run: RunBrief }>('/runs', { body })
      await this.refresh()
      return r.run
    },
    async plan(root: string, env: string, destroy = false): Promise<RunBrief> {
      const r = await api<{ run: RunBrief }>('/runs', { body: { kind: 'tf.plan', root, env, destroy } })
      await this.refresh()
      return r.run
    },
    cancel: (id: number) => api(`/runs/${id}/cancel`, { method: 'POST', body: {} }),
    approve: (id: number, confirm_text: string, acknowledge_danger = false) =>
      api(`/runs/${id}/approve`, { method: 'POST', body: { confirm_text, acknowledge_danger } }),
    init() {
      const later = () => {
        window.clearTimeout(timer)
        timer = window.setTimeout(() => this.refresh().catch(swallow('runs refresh after event')), 150)
      }
      onEvent('run.updated', later)
      onEvent('reconnected', later)
      onEvent('workspace.updated', later)
      onEvent('drift.updated', later)
    },
  },
})

export const STATUS_LABEL: Record<string, string> = {
  queued: 'queued',
  running: 'running',
  waiting_approval: 'needs approval',
  succeeded: 'succeeded',
  failed: 'failed',
  cancelled: 'cancelled',
}
export const STATUS_CLASS: Record<string, string> = {
  queued: 'idle',
  running: 'run',
  waiting_approval: 'warn',
  succeeded: 'ok',
  failed: 'fail',
  cancelled: 'idle',
}

export function kindLabel(r: Pick<RunBrief, 'kind' | 'stage' | 'apply' | 'status'> & { target?: RunBrief['target'] }): string {
  const t = r.target
  switch (r.kind) {
    case 'ans.check':
      return `ansible-playbook ${t?.playbook ?? ''} --check`
    case 'ans.playbook':
      return `ansible-playbook ${t?.playbook ?? ''}`
    case 'ans.ping':
      return 'ansible ping'
    case 'ans.facts':
      return 'ansible gather facts'
    case 'ans.adhoc':
      return `ansible -m ${t?.module ?? ''}${t?.args ? ' ' + t.args : ''}`
  }
  if (r.kind === 'tf.init') return 'terraform init'
  if (r.kind === 'tf.drift') return 'terraform plan -refresh-only'
  const applying = r.apply || r.stage === 'apply' || (r.stage === 'approve' && r.status !== 'waiting_approval' && r.status !== 'cancelled')
  if (t?.destroy) return applying ? 'terraform destroy' : 'terraform plan -destroy'
  return applying ? 'terraform apply' : 'terraform plan'
}

export function duration(from?: string, to?: string, now = Date.now()): string {
  if (!from) return ''
  const ms = (to ? new Date(to).getTime() : now) - new Date(from).getTime()
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return m < 60 ? `${m}m ${s % 60}s` : `${Math.floor(m / 60)}h ${m % 60}m`
}

export function ago(iso: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000))
  if (s < 10) return 'now'
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86400)}d`
}

/** ago() as a phrase: "just now", "5m ago". */
export function since(iso: string, now = Date.now()): string {
  const a = ago(iso, now)
  return a === 'now' ? 'just now' : a + ' ago'
}

/** "failed 14:30:21 after 2m 41s": how a finished run reads in its header. */
export function finishLine(r: Pick<RunBrief, 'status' | 'created_at' | 'started_at' | 'ended_at'>): string {
  if (!r.ended_at) return ''
  const verb = r.status === 'failed' ? 'failed' : r.status === 'cancelled' ? 'cancelled' : 'finished'
  const at = new Date(r.ended_at).toLocaleTimeString([], { hour12: false })
  return `${verb} ${at} after ${duration(r.started_at ?? r.created_at, r.ended_at)}`
}

// ---- typical duration ----

/** Which part of a run its clock covers: up to approval, after it, or the whole run (no approval step). */
export type Phase = 'before' | 'after' | 'all'

export function runPhase(stages: RunStage[]): Phase {
  const approve = stages.find((s) => s.name === 'approve')
  if (!approve) return 'all'
  return approve.status === 'succeeded' ? 'after' : 'before'
}

const secs = (s: RunStage) => (s.started_at && s.ended_at ? Math.max(0, (Date.parse(s.ended_at) - Date.parse(s.started_at)) / 1000) : 0)
const passed = (s: RunStage) => s.status === 'succeeded' || s.status === 'skipped'

/**
 * How long a finished run spent working in a phase, waiting for approval left
 * out, or null when it can't stand as a sample (it failed, or never got there).
 */
export function sampleSeconds(stages: RunStage[], phase: Phase): number | null {
  const i = stages.findIndex((s) => s.name === 'approve')
  const part = phase === 'before' && i >= 0 ? stages.slice(0, i) : stages.filter((s) => s.name !== 'approve')
  if (!part.length || !part.every(passed)) return null
  if (phase === 'after' && stages[i]?.status !== 'succeeded') return null
  const total = part.reduce((n, s) => n + secs(s), 0)
  return total > 0 ? total : null
}

/** Earlier runs that did the same work: same kind on the same root (or playbook) and env, newest first. */
export function similarRuns(list: RunBrief[], run: RunBrief, limit = 8): RunBrief[] {
  const t = run.target
  const same = (o: RunBrief) =>
    o.id !== run.id &&
    o.kind === run.kind &&
    o.target.env === t.env &&
    (o.target.root ?? '') === (t.root ?? '') &&
    (o.target.workspace ?? '') === (t.workspace ?? '') &&
    (o.target.project ?? '') === (t.project ?? '') &&
    (o.target.playbook ?? '') === (t.playbook ?? '') &&
    (o.target.module ?? '') === (t.module ?? '') &&
    o.status !== 'queued' &&
    o.status !== 'running'
  return list
    .filter(same)
    .sort((a, b) => b.id - a.id)
    .slice(0, limit)
}

/** "usually 2–3 min" from past durations in seconds; '' with fewer than two. */
export function typicalRange(samples: number[]): string {
  if (samples.length < 2) return ''
  const s = [...samples].sort((a, b) => a - b)
  const xs = s.length >= 4 ? s.slice(1, -1) : s // one odd run either way doesn't set the range
  const lo = xs[0]
  const hi = xs[xs.length - 1]
  const range = (a: number, b: number, unit: string) => (a === b ? `usually about ${a}${unit}` : `usually ${a}–${b}${unit}`)
  if (hi < 90) {
    const r = (n: number) => Math.max(5, Math.round(n / 5) * 5)
    return range(r(lo), r(hi), 's')
  }
  if (hi < 90 * 60) {
    const a = Math.max(1, Math.round(lo / 60))
    return range(a, Math.max(a, Math.round(hi / 60)), ' min')
  }
  const a = Math.max(1, Math.round(lo / 3600))
  return range(a, Math.max(a, Math.round(hi / 3600)), ' h')
}

// ---- failures ----

export interface LockInfo {
  id?: string
  path?: string
  operation?: string
  who?: string
  version?: string
  /** epoch ms, when the lock's Created time parsed */
  created?: number
}

/** The Lock Info block Terraform prints with a state-lock error, or null when there is none. */
export function parseLockInfo(text: string): LockInfo | null {
  if (!/state lock/i.test(text) || !/Lock Info/i.test(text)) return null
  const field = (k: string) => text.match(new RegExp(`^\\s*${k}:[ \\t]*(\\S.*?)\\s*$`, 'm'))?.[1]
  const info: LockInfo = { id: field('ID'), path: field('Path'), operation: field('Operation'), who: field('Who'), version: field('Version') }
  const c = field('Created')
  if (c) info.created = parseGoTime(c)
  return info.who || info.id || info.created ? info : null
}

/** Go's time.String() / "2006-01-02 15:04:05 UTC" forms, read as UTC unless an offset is given. */
export function parseGoTime(s: string): number | undefined {
  const m = s.match(/^(\d{4}-\d\d-\d\d)[ T](\d\d:\d\d:\d\d)(\.\d+)?\s*([+-]\d{2}:?\d{2}|Z)?/)
  if (!m) return undefined
  const off = !m[4] || m[4] === 'Z' ? 'Z' : m[4].includes(':') ? m[4] : m[4].slice(0, 3) + ':' + m[4].slice(3)
  const ms = m[3] ? m[3].slice(0, 4) : ''
  const t = Date.parse(`${m[1]}T${m[2]}${ms}${off}`)
  return Number.isNaN(t) ? undefined : t
}

/** "OperationTypeApply" → "apply". */
export const lockOperation = (op?: string) => (op ? op.replace(/^OperationType/, '').toLowerCase() : '')

/** "9 min", "2 h", "3 d": how long something has been going on. */
export function heldFor(from: number, now = Date.now()): string {
  const m = Math.max(0, Math.floor((now - from) / 60_000))
  if (m < 1) return 'under a minute'
  if (m < 120) return `${m} min`
  if (m < 48 * 60) return `${Math.floor(m / 60)} h`
  return `${Math.floor(m / 1440)} d`
}

const ANSI = new RegExp(String.fromCharCode(27) + '\\[[0-9;]*m', 'g')

/** The first line of a run error, without exit codes, diagnostic frames or repeated "Error:" prefixes. */
export function cleanError(text: string): string {
  const lines = text
    .replace(ANSI, '')
    .split('\n')
    .map((l) => l.replace(/^[\s│╷╵]+/, '').trim())
  const first = lines.find((l) => l) ?? ''
  return first
    .replace(/exit status \d+:\s*/g, '')
    .replace(/(^|:\s*)Error:\s*/g, '$1')
    .replace(/Error message:\s*/g, '')
    .replace(/\s+/g, ' ')
    .trim()
}

// finished runs' stages don't change, so one fetch each is enough
const stageCache = new Map<number, RunStage[]>()

/** "usually 2–3 min" for a run in the given phase, from the stages of up to five similar finished runs. */
export async function typicalDuration(list: RunBrief[], run: RunBrief, phase: Phase): Promise<string> {
  const samples: number[] = []
  for (const o of similarRuns(list, run)) {
    let stages = stageCache.get(o.id)
    if (!stages) {
      try {
        stages = (await api<{ stages: RunStage[] }>(`/runs/${o.id}`)).stages
      } catch {
        continue
      }
      if (o.status !== 'waiting_approval') stageCache.set(o.id, stages)
    }
    const s = sampleSeconds(stages, phase)
    if (s != null) samples.push(s)
    if (samples.length >= 5) break
  }
  return typicalRange(samples)
}
