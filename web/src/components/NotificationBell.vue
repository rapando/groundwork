<script setup lang="ts">
// The bell: unread count across every project, and the notification list
// behind it, grouped by day. Inside a project and on the project list alike.
import { computed, nextTick, onBeforeUnmount, ref } from 'vue'
import { useRouter } from 'vue-router'
import type { Notification } from '../api/types'
import { useNotifications } from '../stores/notifications'
import { clock, groupByDay, notificationPath } from '../composables/notifyText'
import { projectId } from '../project'
import { swallow } from '../api/swallow'

const props = defineProps<{ align?: 'left' | 'right' }>()
const router = useRouter()
const store = useNotifications()

const open = ref(false)
const btn = ref<HTMLButtonElement | null>(null)
const pop = ref<HTMLElement | null>(null)
const pos = ref({ top: 0, left: 0 })
const WIDTH = 380

// the service list covers every project; this project's list stands in if it couldn't load
const list = computed(() => (store.serviceLoaded.value || !projectId ? store.serviceItems.value : store.items.value))
const count = computed(() => (store.serviceLoaded.value || !projectId ? store.serviceUnread.value : store.unread.value))
const groups = computed(() => groupByDay(list.value))
const other = (n: Notification) => !!n.project && n.project !== projectId
const chip = (n: Notification) => (projectId && !other(n) ? '' : n.project_name || n.project || '')
const href = (n: Notification) => (other(n) || !projectId ? `/p/${n.project}${notificationPath(n)}` : router.resolve(notificationPath(n)).href)

function place() {
  const r = btn.value?.getBoundingClientRect()
  if (!r) return
  const w = Math.min(WIDTH, window.innerWidth - 32)
  const left = props.align === 'right' ? r.right - w : r.left
  pos.value = { top: r.bottom + 8, left: Math.max(16, Math.min(left, window.innerWidth - w - 16)) }
}

async function show() {
  place()
  open.value = true
  store.load().catch(swallow('notifications on open'))
  await nextTick()
  pop.value?.querySelector<HTMLElement>('button:not(:disabled), a')?.focus()
  document.addEventListener('mousedown', outside)
  window.addEventListener('resize', place)
}
function hide(refocus = true) {
  open.value = false
  document.removeEventListener('mousedown', outside)
  window.removeEventListener('resize', place)
  if (refocus) btn.value?.focus()
}
const toggle = () => (open.value ? hide() : show())
function outside(e: MouseEvent) {
  const t = e.target as Node
  if (!pop.value?.contains(t) && !btn.value?.contains(t)) hide(false)
}
// tabbing out of the list closes it
function focusOut(e: FocusEvent) {
  const t = e.relatedTarget as Node | null
  if (t && !pop.value?.contains(t) && !btn.value?.contains(t)) hide(false)
}
onBeforeUnmount(() => {
  document.removeEventListener('mousedown', outside)
  window.removeEventListener('resize', place)
})

async function go(e: MouseEvent, n: Notification) {
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return // let the browser open a new tab
  e.preventDefault()
  hide(false)
  await store.markRead(n).catch(swallow('mark notification read'))
  if (projectId && !other(n)) router.push(notificationPath(n)).catch(swallow('open notification'))
  else location.assign(href(n))
}

const markAll = () => store.markServiceAllRead().catch(swallow('mark all read'))
</script>

<template>
  <button ref="btn" class="bell" type="button" :aria-label="count ? `Notifications, ${count} unread` : 'Notifications'" aria-haspopup="dialog" :aria-expanded="open"
    :class="{ on: open }" @click="toggle" @keydown.esc="open && hide()">
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9" /><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0" /></svg>
    <span v-if="count" class="mono count" aria-hidden="true">{{ count > 99 ? '99+' : count }}</span>
  </button>
  <Teleport to="body">
    <div v-if="open" ref="pop" class="inbox" role="dialog" aria-label="Notifications" :style="{ top: pos.top + 'px', left: pos.left + 'px' }"
      @keydown.esc.stop="hide()" @focusout="focusOut">
      <div class="ihead">
        <h2>Notifications</h2>
        <button class="link" type="button" :disabled="!count" @click="markAll">Mark all read</button>
      </div>
      <p v-if="!list.length" class="empty muted">Nothing yet. Failed runs, plans waiting for approval and drift show up here.</p>
      <div v-else class="ilist">
        <section v-for="g in groups" :key="g.day" :aria-label="g.day">
          <h3 class="lbl">{{ g.day }}</h3>
          <ul>
            <li v-for="n in g.items" :key="(n.project ?? '') + n.id">
              <a class="item" :class="{ unread: !n.read_at }" :href="href(n)" @click="go($event, n)">
                <span class="udot" :class="'ev-' + n.event" aria-hidden="true" />
                <span class="itext">
                  <span class="irow"><span class="ititle">{{ n.title }}</span><span class="mono time">{{ clock(n.at) }}</span></span>
                  <span class="irow sub"><span v-if="chip(n)" class="mono chip">{{ chip(n) }}</span><span class="mono detail">{{ n.cause?.title || n.text }}</span></span>
                </span>
                <span v-if="!n.read_at" class="sr">unread</span>
              </a>
            </li>
          </ul>
        </section>
      </div>
      <div class="ifoot">
        <span class="muted">Kept for 30 days</span>
        <RouterLink to="/notifications" @click="hide(false)">Notification settings →</RouterLink>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.bell { position: relative; display: inline-grid; place-items: center; width: 32px; height: 32px; border: 1px solid transparent; border-radius: var(--r-control); background: transparent; color: var(--text-soft); cursor: pointer; }
.bell:hover, .bell.on { background: var(--bg-hover); color: var(--text); border-color: var(--line-strong); }
.count { position: absolute; top: -4px; right: -6px; min-width: 18px; height: 18px; padding: 0 5px; border-radius: 9px; background: var(--fail); color: var(--on-accent); font-size: 11px; font-weight: 700; line-height: 18px; text-align: center; }
.inbox { position: fixed; z-index: 95; width: min(380px, calc(100vw - 32px)); max-height: min(560px, calc(100vh - 80px)); display: flex; flex-direction: column; background: var(--bg-panel); border: 1px solid var(--line-strong); border-radius: var(--r-panel); box-shadow: var(--shadow); overflow: hidden; }
.ihead { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 14px; border-bottom: 1px solid var(--line); }
.ihead h2 { margin: 0; font-size: 15px; font-weight: 600; }
.link { border: 0; background: transparent; color: var(--accent); font-size: 13px; cursor: pointer; padding: 4px 2px; }
.link:hover:not(:disabled) { color: var(--accent-hover); text-decoration: underline; }
.link:disabled { color: var(--text-disabled); cursor: default; }
.empty { margin: 0; padding: 18px 14px; font-size: 14px; line-height: 1.5; }
.ilist { overflow-y: auto; padding: 4px 6px 8px; }
h3 { margin: 0; padding: 10px 8px 4px; }
ul { list-style: none; margin: 0; padding: 0; }
.item { display: flex; gap: 10px; padding: 8px; border-radius: var(--r-control); color: var(--text-soft); text-decoration: none; }
.item:hover { background: var(--bg-hover); color: var(--text); }
.item.unread .ititle { color: var(--text); font-weight: 600; }
.udot { flex: none; width: 8px; height: 8px; margin-top: 6px; border-radius: 50%; background: transparent; }
.unread .udot { background: var(--text-muted); }
.unread .udot.ev-failed { background: var(--fail); }
.unread .udot.ev-approval, .unread .udot.ev-drift { background: var(--warn); }
.unread .udot.ev-succeeded { background: var(--ok); }
.itext { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.irow { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
.ititle { flex: 1; min-width: 0; font-size: 14px; }
.time { flex: none; font-size: 13px; color: var(--text-label); }
.sub { font-size: 13px; color: var(--text-muted); }
.chip { flex: none; padding: 0 6px; border: 1px solid var(--line-strong); border-radius: 4px; font-size: 12px; color: var(--text-soft); }
.detail { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ifoot { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 14px; border-top: 1px solid var(--line); font-size: 13px; }
.ifoot a { text-decoration: none; }
.ifoot a:hover { text-decoration: underline; }
.sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
</style>
