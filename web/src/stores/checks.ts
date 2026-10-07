import { defineStore } from 'pinia'
import { api } from '../api/client'
import type { ChecksResponse, Diagnostic, UnitStatus } from '../api/types'
import { onEvent } from '../composables/events'

export interface Badge { errors: number; warnings: number }

export const useChecks = defineStore('checks', {
  state: () => ({
    diagnostics: [] as Diagnostic[],
    units: [] as UnitStatus[],
    counts: { error: 0, warning: 0, info: 0 },
    loaded: false,
    error: '',
  }),
  getters: {
    /** Error/warning counts per file and per ancestor directory, for tree badges. */
    badges(state): Map<string, Badge> {
      const m = new Map<string, Badge>()
      const bump = (p: string, sev: string) => {
        const b = m.get(p) ?? { errors: 0, warnings: 0 }
        if (sev === 'error') b.errors++
        else if (sev === 'warning') b.warnings++
        m.set(p, b)
      }
      for (const d of state.diagnostics) {
        const parts = d.file.split('/')
        for (let i = 1; i <= parts.length; i++) bump(parts.slice(0, i).join('/'), d.severity)
      }
      return m
    },
    running: (s) => s.units.some((u) => u.running),
    forFile: (s) => (file: string) => s.diagnostics.filter((d) => d.file === file),
    unitFor: (s) => (file: string): UnitStatus | undefined => {
      let best: UnitStatus | undefined
      for (const u of s.units) {
        if (file === u.path || file.startsWith(u.path + '/') || u.path === '.') {
          if (!best || u.path.length > best.path.length) best = u
        }
      }
      return best
    },
  },
  actions: {
    async refresh() {
      try {
        const r = await api<ChecksResponse>('/checks')
        this.diagnostics = r.diagnostics
        this.units = r.units
        this.counts = r.counts
        this.error = ''
      } catch (e) {
        this.error = e instanceof Error ? e.message : String(e)
      } finally {
        this.loaded = true
      }
    },
    async run(unit = '', force = false) {
      await api('/checks/run', { body: { unit, force } })
    },
    init() {
      onEvent('checks.started', () => this.refresh())
      onEvent('checks.updated', () => this.refresh())
      onEvent('workspace.updated', () => this.refresh())
      onEvent('reconnected', () => this.refresh())
    },
  },
})
