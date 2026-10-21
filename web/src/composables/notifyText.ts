// Pure helpers for run notifications: the words and links a notification
// turns into in toasts, the inbox, the tab title and desktop notifications.
import type { Notification } from '../api/types'

/** The short name a tab title uses: the playbook file, or "apply #14 on prod". */
export function shortLabel(n: Pick<Notification, 'label' | 'run_id' | 'env' | 'event'>): string {
  if (n.event === 'drift') return n.env
  const file = n.label.match(/(\S+\.ya?ml)\b/)
  if (file) return file[1]
  const verb = n.label.replace(/^(terraform|tofu|ansible)\s+/, '') || 'run'
  return `${verb} #${n.run_id}${n.env ? ' on ' + n.env : ''}`
}

/** The status word at the start of the tab title. */
export function statusWord(n: Pick<Notification, 'event' | 'stage' | 'kind'>): string {
  switch (n.event) {
    case 'failed':
      return '✕ Failed'
    case 'approval':
      return '● Needs approval'
    case 'succeeded':
      return n.stage === 'apply' || n.kind === 'tf.apply' ? '✓ Applied' : '✓ Succeeded'
    case 'cancelled':
      return '– Cancelled'
    case 'drift':
      return '● Drift'
  }
}

/** "✕ Failed · site.yml · groundwork": the title of a background tab. */
export function tabTitle(n: Pick<Notification, 'event' | 'stage' | 'kind' | 'label' | 'run_id' | 'env'>): string {
  return `${statusWord(n)} · ${shortLabel(n)} · groundwork`
}

/** The colour token for the favicon dot. */
export function dotToken(event: Notification['event']): string {
  if (event === 'failed') return '--fail'
  if (event === 'succeeded') return '--ok'
  if (event === 'cancelled') return '--text-muted'
  return '--warn'
}

/** Where a notification leads, as a path inside its project. */
export function notificationPath(n: Pick<Notification, 'event' | 'run_id' | 'root' | 'env'>): string {
  if (n.event === 'approval') return `/runs/${n.run_id}/plan`
  if (n.event === 'drift') {
    const q = new URLSearchParams()
    if (n.root) q.set('root', n.root)
    if (n.env) q.set('env', n.env)
    q.set('filter', 'drifted')
    return '/graph?' + q.toString()
  }
  return `/runs/${n.run_id}`
}

/** The run a route path shows, from /runs/14 or /runs/14/plan; 0 elsewhere. */
export function runOnPath(path: string): number {
  const m = path.match(/^\/runs\/(\d+)(\/plan)?\/?$/)
  return m ? Number(m[1]) : 0
}

/** Whether a toast should fade on its own: successes and cancellations do, the rest wait for you. */
export function fades(event: Notification['event']): boolean {
  return event === 'succeeded' || event === 'cancelled'
}

const pad = (n: number) => String(n).padStart(2, '0')

/** "14:30", local time. */
export function clock(at: string): string {
  const d = new Date(at)
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function dayKey(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** Groups a newest-first list by local day: "Today", "Yesterday", then "Mon 19 Oct". */
export function groupByDay<T extends { at: string }>(items: T[], now = Date.now()): { day: string; items: T[] }[] {
  const today = new Date(now)
  const yesterday = new Date(today.getFullYear(), today.getMonth(), today.getDate() - 1)
  const names = new Map([
    [dayKey(today), 'Today'],
    [dayKey(yesterday), 'Yesterday'],
  ])
  const out: { day: string; items: T[] }[] = []
  for (const it of items) {
    const d = new Date(it.at)
    const day = names.get(dayKey(d)) ?? d.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short' })
    if (!out.length || out[out.length - 1].day !== day) out.push({ day, items: [] })
    out[out.length - 1].items.push(it)
  }
  return out
}

/** You are watching a run when its page is open in a visible tab: no toast or desktop for it then. */
export function watching(runId: number, hidden: boolean, currentRun: number): boolean {
  return !hidden && currentRun === runId
}

const RANK: Record<Notification['event'], number> = { failed: 4, approval: 3, drift: 2, succeeded: 1, cancelled: 0 }
/** Whether a new event should replace what the tab title already says. */
export function outranks(next: Notification['event'], current: Notification['event'] | null): boolean {
  return current === null || RANK[next] >= RANK[current]
}
