// One SSE connection for the whole app. Stores subscribe with onEvent();
// after a reconnect, 'reconnected' fires so they can refetch snapshots.
import { apiBase } from '../project'

type Handler = (data: any) => void

const handlers = new Map<string, Set<Handler>>()
const TYPES = ['file.changed', 'checks.started', 'checks.updated', 'workspace.updated', 'run.updated', 'log.line', 'ans.result', 'drift.updated', 'issues.updated', 'doctor.updated']
let es: EventSource | null = null
let backoff = 500
let reconnecting = false
let started = false

export function onEvent(type: string, h: Handler): () => void {
  if (!handlers.has(type)) handlers.set(type, new Set())
  handlers.get(type)!.add(h)
  return () => handlers.get(type)?.delete(h)
}

function emit(type: string, data: unknown) {
  handlers.get(type)?.forEach((h) => {
    try { h(data) } catch (e) { console.error('event handler failed', type, e) }
  })
}

function connect() {
  es = new EventSource(apiBase + '/events')
  es.addEventListener('hello', () => {
    if (reconnecting) emit('reconnected', null)
    reconnecting = false
    backoff = 500
  })
  for (const t of TYPES) {
    es.addEventListener(t, (e) => {
      let data: unknown = null
      try { data = JSON.parse((e as MessageEvent).data) } catch { /* empty payload */ }
      emit(t, data)
    })
  }
  es.onerror = () => {
    es?.close()
    reconnecting = true
    setTimeout(connect, backoff)
    backoff = Math.min(backoff * 2, 10_000)
  }
}

export function startEvents() {
  if (started) return
  started = true
  connect()
}
