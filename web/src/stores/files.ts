import { defineStore } from 'pinia'
import { api } from '../api/client'
import type { FilesResponse } from '../api/types'
import { onEvent } from '../composables/events'

function load(key: string, fallback: string): string {
  try { return localStorage.getItem(key) ?? fallback } catch { return fallback }
}
function save(key: string, v: string) {
  try { localStorage.setItem(key, v) } catch { /* private mode */ }
}

let timer: number | undefined

export const useFiles = defineStore('files', {
  state: () => ({
    files: [] as string[],
    sections: [] as FilesResponse['sections'],
    truncated: false,
    iacOnly: load('gw.iacOnly', '1') === '1',
    loaded: false,
  }),
  actions: {
    async refresh() {
      const r = await api<FilesResponse>('/files' + (this.iacOnly ? '?iac_only=1' : ''))
      this.files = r.files
      this.sections = r.sections
      this.truncated = r.truncated
      this.loaded = true
    },
    async setIacOnly(v: boolean) {
      this.iacOnly = v
      save('gw.iacOnly', v ? '1' : '0')
      await this.refresh()
    },
    init() {
      // file list only changes when files are created/removed; coalesce bursts
      onEvent('file.changed', () => {
        window.clearTimeout(timer)
        timer = window.setTimeout(() => this.refresh().catch(() => {}), 600)
      })
      onEvent('workspace.updated', () => this.refresh().catch(() => {}))
      onEvent('reconnected', () => this.refresh().catch(() => {}))
    },
  },
})
