import { onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { api } from '../api/client'
import type { LogLine, LogPage, ResEvent } from '../api/types'
import { onEvent } from './events'

const PAGE = 2000
const INITIAL_MAX = 20000
const RANK: Record<string, number> = { debug: 0, info: 1, warn: 2, error: 3 }

/**
 * Loads a run's log (the tail, if huge) and then follows live `log.line`
 * events. Filters are applied by the server for history and by the client for
 * live lines, so both views agree. Resource progress is reported for every
 * live line, even ones a filter hides.
 */
export function useRunLog(runId: Ref<number | null>, level: Ref<string>, query: Ref<string>, onRes: (r: ResEvent) => void) {
  const lines = ref<LogLine[]>([])
  const total = ref(0)
  const loading = ref(false)
  const skipped = ref(0) // lines before the loaded tail

  let ready = false
  let buffer: LogLine[] = []
  let maxSeen = -1
  let gen = 0

  const matches = (l: LogLine) =>
    (RANK[l.level] ?? 1) >= (RANK[level.value] ?? 0) && (!query.value || l.text.toLowerCase().includes(query.value.toLowerCase()))

  function accept(batch: LogLine[]) {
    const fresh: LogLine[] = []
    for (const l of batch) {
      if (l.n <= maxSeen) continue
      maxSeen = l.n
      total.value = Math.max(total.value, l.n + 1)
      if (matches(l)) fresh.push(l)
    }
    if (fresh.length) lines.value = lines.value.concat(fresh)
  }

  async function load(all = false) {
    const id = runId.value
    const mine = ++gen
    ready = false
    buffer = []
    lines.value = []
    maxSeen = -1
    skipped.value = 0
    if (id == null) return
    loading.value = true
    const q = `&level=${encodeURIComponent(level.value)}&q=${encodeURIComponent(query.value)}`
    try {
      const probe = await api<LogPage>(`/runs/${id}/log?from=0&limit=1${q}`)
      let from = all ? 0 : Math.max(0, probe.total - INITIAL_MAX)
      total.value = probe.total
      skipped.value = from
      const got: LogLine[] = []
      for (;;) {
        const page = await api<LogPage>(`/runs/${id}/log?from=${from}&limit=${PAGE}${q}`)
        got.push(...page.lines)
        if (page.lines.length < PAGE) break
        from = page.next
      }
      if (mine !== gen) return
      lines.value = got
      maxSeen = Math.max(probe.total - 1, got.length ? got[got.length - 1].n : -1)
    } finally {
      if (mine === gen) loading.value = false
    }
    if (mine !== gen) return
    ready = true
    accept(buffer) // events that arrived while loading
    buffer = []
  }

  const off = onEvent('log.line', (d: { run: number; lines: LogLine[] }) => {
    if (d.run !== runId.value) return
    for (const l of d.lines) if (l.res) onRes(l.res)
    if (!ready) buffer.push(...d.lines)
    else accept(d.lines)
  })
  onBeforeUnmount(off)

  let t: number | undefined
  watch(runId, () => load(), { immediate: true })
  watch([level, query], () => { window.clearTimeout(t); t = window.setTimeout(() => load(), 250) })
  onEvent('reconnected', () => load())

  return { lines, total, loading, skipped, reload: load }
}
