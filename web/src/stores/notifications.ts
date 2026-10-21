// Run notifications: this project's list and unread count, the service-wide
// list behind the bell, and live updates from the project event stream.
// One shared state for the page; components and other stores import
// useNotifications() and get the same refs.
import { computed, ref } from 'vue'
import { api, serviceApi } from '../api/client'
import type { Notification, NotificationsResponse } from '../api/types'
import { onEvent } from '../composables/events'
import { projectId } from '../project'
import { swallow } from '../api/swallow'

const items = ref<Notification[]>([])
const unread = ref(0)
const serviceItems = ref<Notification[]>([])
const serviceUnread = ref(0)
const loaded = ref(false)
const serviceLoaded = ref(false)

const unreadRuns = computed(() => new Set(items.value.filter((n) => !n.read_at).map((n) => n.run_id)))
/** unread failures in this project: the Runs badge */
const unreadFailed = computed(() => items.value.filter((n) => n.event === 'failed' && !n.read_at).length)

type Listener = (n: Notification) => void
const listeners = new Set<Listener>()

/** Calls fn for every notification that arrives live; returns the unsubscribe. */
export function onNotification(fn: Listener): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

async function loadProject() {
  const r = await api<NotificationsResponse>('/notifications?limit=50')
  items.value = r.items ?? []
  unread.value = r.unread
  loaded.value = true
}

async function loadService() {
  const r = await serviceApi<NotificationsResponse>('/notifications?limit=50')
  serviceItems.value = r.items ?? []
  serviceUnread.value = r.unread
  serviceLoaded.value = true
}

/** Loads this project's list (in a project) and the service-wide list. */
async function load() {
  await Promise.all([projectId ? loadProject() : Promise.resolve(), loadService()])
}

const stamp = () => new Date().toISOString()
function markLocal(list: typeof items, match: (n: Notification) => boolean) {
  let changed = 0
  list.value = list.value.map((n) => {
    if (n.read_at || !match(n)) return n
    changed++
    return { ...n, read_at: stamp() }
  })
  return changed
}
const inThisProject = (n: Notification) => !n.project || n.project === projectId

/** Marks everything about a run read: the run page calls it when you open the run. */
async function markRunRead(runId: number) {
  if (!projectId) return
  // nothing unread for it here: skip the request (unless the list hasn't loaded yet)
  if (loaded.value && !unreadRuns.value.has(runId)) return
  markLocal(items, (n) => n.run_id === runId)
  const n = markLocal(serviceItems, (x) => inThisProject(x) && x.run_id === runId)
  serviceUnread.value = Math.max(0, serviceUnread.value - n)
  const r = await api<{ unread: number }>('/notifications/read', { body: { run_id: runId } })
  unread.value = r.unread
  loadService().catch(swallow('service notifications after read'))
}

/** Marks one notification read, in whichever project it belongs to. */
async function markRead(n: Notification) {
  if (n.read_at) return
  const other = n.project && n.project !== projectId
  markLocal(items, (x) => x.id === n.id && !other)
  if (markLocal(serviceItems, (x) => x.id === n.id && (x.project ?? projectId) === (n.project ?? projectId)))
    serviceUnread.value = Math.max(0, serviceUnread.value - 1)
  // the project API lives under /api/p/<id>, so the service client reaches any project
  const r = other
    ? await serviceApi<{ unread: number }>(`/p/${n.project}/notifications/read`, { body: { ids: [n.id] } })
    : await api<{ unread: number }>('/notifications/read', { body: { ids: [n.id] } })
  if (!other) unread.value = r.unread
}

/** Marks this project's list read (on the project list: every project's). */
async function markAllRead() {
  if (!projectId) return markServiceAllRead()
  markLocal(items, () => true)
  const r = await api<{ unread: number }>('/notifications/read', { body: { all: true } })
  unread.value = r.unread
  await loadService().catch(swallow('service notifications after read'))
}

/** Marks every project's notifications read: the bell's "Mark all read". */
async function markServiceAllRead() {
  markLocal(serviceItems, () => true)
  markLocal(items, () => true)
  serviceUnread.value = 0
  const r = await serviceApi<{ unread: number }>('/notifications/read', { body: { all: true } })
  serviceUnread.value = r.unread
  if (projectId) unread.value = 0
}

function receive(n: Notification) {
  if (!n || typeof n.id !== 'number') return
  if (items.value.some((x) => x.id === n.id)) return
  items.value = [n, ...items.value]
  if (!n.read_at) {
    unread.value++
    serviceUnread.value++
  }
  serviceItems.value = [{ ...n, project: projectId }, ...serviceItems.value]
  for (const fn of listeners) {
    try {
      fn(n)
    } catch (e) {
      console.error('notification listener failed', e)
    }
  }
}

let started = false
let poll = 0
/** Subscribes to the event stream and polls the service list; once per page. */
export function startNotifications(pollMs = 30_000) {
  if (started) return
  started = true
  load().catch(swallow('initial notifications'))
  if (projectId) {
    onEvent('notification.new', receive)
    onEvent('notifications.updated', (d: { unread?: number } | null) => {
      if (typeof d?.unread === 'number') unread.value = d.unread
      loadProject().catch(swallow('notifications after update'))
    })
    onEvent('reconnected', () => load().catch(swallow('notifications after reconnect')))
  }
  poll = window.setInterval(() => loadService().catch(swallow('service notifications poll')), pollMs)
}

/** For tests: forget the state and stop polling. */
export function resetNotifications() {
  window.clearInterval(poll)
  started = false
  items.value = []
  serviceItems.value = []
  unread.value = 0
  serviceUnread.value = 0
  loaded.value = false
  serviceLoaded.value = false
  listeners.clear()
}

export function useNotifications() {
  return {
    items,
    unread,
    serviceItems,
    serviceUnread,
    unreadRuns,
    unreadFailed,
    loaded,
    serviceLoaded,
    markRunRead,
    markRead,
    markAllRead,
    markServiceAllRead,
    load,
    loadService,
  }
}
