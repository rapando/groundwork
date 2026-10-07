import { defineStore } from 'pinia'
import { api } from '../api/client'
import type { Workspace } from '../api/types'

export const useWorkspace = defineStore('workspace', {
  state: () => ({ ws: null as Workspace | null, error: '' }),
  actions: {
    async refresh() {
      try {
        this.ws = await api<Workspace>('/workspace')
        this.error = ''
      } catch (e) {
        this.error = e instanceof Error ? e.message : String(e)
      }
      return this.ws
    },
  },
})
