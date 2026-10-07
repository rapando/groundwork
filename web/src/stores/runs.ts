import { defineStore } from 'pinia'
import { api } from '../api/client'
import type { EnvView, RunBrief } from '../api/types'
import { onEvent } from '../composables/events'

let timer: number | undefined

export const useRuns = defineStore('runs', {
  state: () => ({ list: [] as RunBrief[], envs: [] as EnvView[], loaded: false }),
  getters: {
    live: (s) => s.list.filter((r) => r.status === 'running' || r.status === 'queued').length,
    awaiting: (s) => s.list.filter((r) => r.status === 'waiting_approval').length,
  },
  actions: {
    async refresh() {
      const [runs, envs] = await Promise.all([
        api<{ runs: RunBrief[] }>('/runs?limit=100'),
        api<{ envs: EnvView[] }>('/envs'),
      ])
      this.list = runs.runs
      this.envs = envs.envs
      this.loaded = true
    },
    async submit(body: Record<string, unknown>): Promise<RunBrief> {
      const r = await api<{ run: RunBrief }>('/runs', { body })
      await this.refresh()
      return r.run
    },
    async plan(root: string, env: string): Promise<RunBrief> {
      const r = await api<{ run: RunBrief }>('/runs', { body: { kind: 'tf.plan', root, env } })
      await this.refresh()
      return r.run
    },
    cancel: (id: number) => api(`/runs/${id}/cancel`, { method: 'POST', body: {} }),
    approve: (id: number, confirm_text: string, acknowledge_danger = false) =>
      api(`/runs/${id}/approve`, { method: 'POST', body: { confirm_text, acknowledge_danger } }),
    init() {
      const later = () => {
        window.clearTimeout(timer)
        timer = window.setTimeout(() => this.refresh().catch(() => {}), 150)
      }
      onEvent('run.updated', later)
      onEvent('reconnected', later)
      onEvent('workspace.updated', later)
      onEvent('drift.updated', later)
    },
  },
})

export const STATUS_LABEL: Record<string, string> = {
  queued: 'queued', running: 'running', waiting_approval: 'needs approval',
  succeeded: 'succeeded', failed: 'failed', cancelled: 'cancelled',
}
export const STATUS_CLASS: Record<string, string> = {
  queued: 'idle', running: 'run', waiting_approval: 'warn', succeeded: 'ok', failed: 'fail', cancelled: 'idle',
}

export function kindLabel(r: Pick<RunBrief, 'kind' | 'stage' | 'apply' | 'status'> & { target?: RunBrief['target'] }): string {
  const t = r.target
  switch (r.kind) {
    case 'ans.check': return `ansible-playbook ${t?.playbook ?? ''} --check`
    case 'ans.playbook': return `ansible-playbook ${t?.playbook ?? ''}`
    case 'ans.ping': return 'ansible ping'
    case 'ans.facts': return 'ansible gather facts'
    case 'ans.adhoc': return `ansible -m ${t?.module ?? ''}${t?.args ? ' ' + t.args : ''}`
  }
  if (r.kind === 'tf.init') return 'terraform init'
  if (r.kind === 'tf.drift') return 'terraform plan -refresh-only'
  const applying = r.apply || r.stage === 'apply' || (r.stage === 'approve' && r.status !== 'waiting_approval' && r.status !== 'cancelled')
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
