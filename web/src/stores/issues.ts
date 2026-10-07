import { defineStore } from 'pinia'
import { api } from '../api/client'
import type { Issue, IssuesResponse } from '../api/types'
import { onEvent } from '../composables/events'

export const useIssues = defineStore('issues', {
  state: () => ({ open: [] as Issue[], resolved: [] as Issue[], resolvedThisWeek: 0, ruleErrors: [] as string[], loaded: false }),
  actions: {
    async refresh() {
      try {
        const r = await api<IssuesResponse>('/issues')
        this.open = r.open
        this.resolved = r.resolved
        this.resolvedThisWeek = r.resolved_this_week
        this.ruleErrors = r.rule_errors ?? []
      } finally { this.loaded = true }
    },
    listen() {
      let t: number | undefined
      const later = () => { window.clearTimeout(t); t = window.setTimeout(() => this.refresh(), 200) }
      onEvent('issues.updated', later)
      onEvent('reconnected', later)
    },
  },
})
