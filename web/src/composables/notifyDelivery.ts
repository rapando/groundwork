// Delivers live notifications: toasts in the console, the tab title and
// favicon dot of a background tab, and browser desktop notifications.
// State lives here, not in a component, because every view mounts its own
// AppShell: toasts and their timers must outlive a route change.
import { ref } from 'vue'
import type { Router } from 'vue-router'
import type { Notification } from '../api/types'
import { onNotification, useNotifications } from '../stores/notifications'
import { projectId } from '../project'
import { dotToken, fades, notificationPath, outranks, runOnPath, tabTitle, watching } from './notifyText'
import { swallow } from '../api/swallow'

// ---- toasts

export interface Toast {
  n: Notification
  /** fades on its own after FADE_MS unless held (hover or focus) */
  fades: boolean
}
export const FADE_MS = 6000
const MAX_TOASTS = 4
export const toasts = ref<Toast[]>([])
const timers = new Map<number, number>()

export function pushToast(n: Notification) {
  if (toasts.value.some((t) => t.n.id === n.id)) return
  const t: Toast = { n, fades: fades(n.event) }
  // newest on top; the oldest fading ones go first when the stack is full
  const next = [t, ...toasts.value]
  while (next.length > MAX_TOASTS) {
    const i = next.map((x) => x.fades).lastIndexOf(true)
    const [gone] = next.splice(i >= 0 ? i : next.length - 1, 1)
    window.clearTimeout(timers.get(gone.n.id))
    timers.delete(gone.n.id)
  }
  toasts.value = next
  if (t.fades) release(n.id)
}

export function dismissToast(id: number) {
  window.clearTimeout(timers.get(id))
  timers.delete(id)
  toasts.value = toasts.value.filter((t) => t.n.id !== id)
}

/** Stops a fading toast from going while the pointer or focus is on it. */
export function holdToast(id: number) {
  window.clearTimeout(timers.get(id))
  timers.delete(id)
}

/** Restarts the fade after a hold. */
export function release(id: number) {
  const t = toasts.value.find((x) => x.n.id === id)
  if (!t?.fades) return
  window.clearTimeout(timers.get(id))
  timers.set(
    id,
    window.setTimeout(() => dismissToast(id), FADE_MS),
  )
}

// ---- tab title and favicon

let baseTitle: string | null = null
let flagged: Notification['event'] | null = null
const icons: { el: HTMLLinkElement; href: string }[] = []

function iconLinks(): HTMLLinkElement[] {
  return [...document.querySelectorAll<HTMLLinkElement>('link[rel~="icon"]')]
}

async function dotFavicon(token: string) {
  const colour = getComputedStyle(document.documentElement).getPropertyValue(token).trim()
  const ring = getComputedStyle(document.documentElement).getPropertyValue('--bg').trim()
  const img = new Image()
  img.src = '/favicon.svg'
  await img.decode()
  const c = document.createElement('canvas')
  c.width = c.height = 64
  const g = c.getContext('2d')
  if (!g) return
  g.drawImage(img, 0, 0, 64, 64)
  g.beginPath()
  g.arc(48, 16, 15, 0, Math.PI * 2)
  g.fillStyle = ring
  g.fill()
  g.beginPath()
  g.arc(48, 16, 11, 0, Math.PI * 2)
  g.fillStyle = colour
  g.fill()
  const url = c.toDataURL('image/png')
  for (const el of iconLinks()) {
    if (!icons.some((i) => i.el === el)) icons.push({ el, href: el.href })
    el.href = url
  }
}

/** Marks a background tab: "✕ Failed · site.yml · groundwork" and a dot on the favicon. */
export function flagTab(n: Notification) {
  if (!document.hidden || !outranks(n.event, flagged)) return
  if (baseTitle === null) baseTitle = document.title
  flagged = n.event
  document.title = tabTitle(n)
  dotFavicon(dotToken(n.event)).catch(swallow('favicon dot'))
}

/** Puts the title and favicon back: called when you look at the tab. */
export function clearTab() {
  if (baseTitle !== null) document.title = baseTitle
  baseTitle = null
  flagged = null
  for (const i of icons.splice(0)) i.el.href = i.href
}

// ---- desktop

export const desktopSupported = () => typeof window !== 'undefined' && 'Notification' in window
export const desktopPermission = ref<NotificationPermission | 'unsupported'>(desktopSupported() ? Notification.permission : 'unsupported')

/** Asks the browser for permission; needs a click to call it from. */
export async function requestDesktop(): Promise<NotificationPermission | 'unsupported'> {
  if (!desktopSupported()) return 'unsupported'
  desktopPermission.value = await Notification.requestPermission()
  return desktopPermission.value
}

function showDesktop(n: Notification, open: (path: string) => void) {
  if (!desktopSupported() || Notification.permission !== 'granted') return
  // the tag folds the copies that several open tabs would show into one
  const d = new Notification(n.title, { body: n.text, tag: `groundwork-${projectId}-${n.id}` })
  d.onclick = () => {
    window.focus()
    open(notificationPath(n))
    d.close()
  }
}

// ---- wiring

let started = false
/** Hooks live notifications up to toasts, the tab and the desktop; once per page. */
export function startDelivery(router: Router) {
  if (started) return
  started = true
  const store = useNotifications()
  const open = (path: string) => {
    router.push(path).catch(swallow('open notification'))
  }
  onNotification((n) => {
    const hidden = document.hidden
    const current = runOnPath(router.currentRoute.value.path)
    if (watching(n.run_id, hidden, current)) {
      // you are on its page: count it as seen
      store.markRunRead(n.run_id).catch(swallow('mark watched run read'))
      return
    }
    if (n.toast) pushToast(n)
    if (n.toast || n.desktop) flagTab(n)
    if (n.desktop) showDesktop(n, open)
  })
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) clearTab()
  })
  window.addEventListener('focus', clearTab)
}
