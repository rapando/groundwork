import { defineStore } from 'pinia'
import { api } from '../api/client'
import type { GitStatus } from '../api/types'
import { onEvent } from '../composables/events'

let timer: number | undefined

export const useGit = defineStore('git', {
  state: () => ({ status: null as GitStatus | null }),
  getters: {
    dirty: (s) => s.status?.files.length ?? 0,
    byPath: (s) => new Map((s.status?.files ?? []).map((f) => [f.path, f])),
  },
  actions: {
    async refresh() {
      try { this.status = await api<GitStatus>('/git/status') } catch { /* git is optional */ }
    },
    init() {
      const later = () => { window.clearTimeout(timer); timer = window.setTimeout(() => this.refresh(), 400) }
      onEvent('file.changed', later)
      onEvent('reconnected', () => this.refresh())
      window.addEventListener('focus', () => this.refresh())
    },
  },
})
