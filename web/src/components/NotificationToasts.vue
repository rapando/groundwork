<script setup lang="ts">
// The toast stack for live run notifications. Failures, approvals and drift
// stay until you dismiss them; successes fade after a few seconds.
import type { Notification } from '../api/types'
import { dismissToast, holdToast, release, toasts } from '../composables/notifyDelivery'
import { notificationPath } from '../composables/notifyText'
import { useNotifications } from '../stores/notifications'
import { swallow } from '../api/swallow'

const store = useNotifications()
const ICON: Record<Notification['event'], string> = { failed: '!', approval: '?', succeeded: '✓', cancelled: '–', drift: '~' }

// acting on a toast counts as reading it
function act(n: Notification) {
  dismissToast(n.id)
  store.markRead(n).catch(swallow('mark toast read'))
}
</script>

<template>
  <ol class="stack" aria-live="polite" aria-label="New notifications">
    <li v-for="t in toasts" :key="t.n.id" class="toast" :class="'ev-' + t.n.event"
      @mouseenter="holdToast(t.n.id)" @mouseleave="release(t.n.id)" @focusin="holdToast(t.n.id)" @focusout="release(t.n.id)">
      <span class="ico mono" aria-hidden="true">{{ ICON[t.n.event] }}</span>
      <div class="body">
        <div class="head">
          <strong class="title">{{ t.n.title }}</strong>
          <button class="x" type="button" :aria-label="`Dismiss: ${t.n.title}`" @click="dismissToast(t.n.id)">✕</button>
        </div>
        <p v-if="t.n.text" class="mono text">{{ t.n.text }}</p>
        <div class="acts">
          <template v-if="t.n.event === 'failed'">
            <RouterLink v-if="t.n.cause" class="btn sm primary" :to="`/troubleshoot/${t.n.cause.issue_id}`" @click="act(t.n)">{{ t.n.cause.action || 'Troubleshoot' }}</RouterLink>
            <RouterLink class="btn sm" :class="{ primary: !t.n.cause }" :to="notificationPath(t.n)" @click="act(t.n)">Open run</RouterLink>
          </template>
          <template v-else-if="t.n.event === 'approval'">
            <RouterLink class="btn sm primary" :to="notificationPath(t.n)" @click="act(t.n)">Review plan</RouterLink>
            <button class="btn sm" type="button" @click="dismissToast(t.n.id)">Later</button>
          </template>
          <RouterLink v-else-if="t.n.event === 'drift'" class="btn sm" :to="notificationPath(t.n)" @click="act(t.n)">Show drifted</RouterLink>
          <RouterLink v-else class="btn sm" :to="notificationPath(t.n)" @click="act(t.n)">Open run</RouterLink>
        </div>
      </div>
    </li>
  </ol>
</template>

<style scoped>
.stack { position: fixed; right: 16px; bottom: 16px; z-index: 90; width: min(400px, calc(100vw - 32px)); margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; gap: 10px; pointer-events: none; }
.toast { pointer-events: auto; display: flex; gap: 12px; padding: 14px 14px 14px 16px; border: 1px solid var(--line-strong); border-left: 3px solid var(--tone); border-radius: var(--r-card); background: var(--bg-panel); box-shadow: var(--shadow); --tone: var(--text-muted); animation: rise .18s ease-out; }
.ev-failed { --tone: var(--fail); }
.ev-approval, .ev-drift { --tone: var(--warn); }
.ev-succeeded { --tone: var(--ok); }
.ico { flex: none; width: 24px; height: 24px; border-radius: 50%; display: grid; place-items: center; font-size: 13px; font-weight: 700; color: var(--tone); background: color-mix(in srgb, var(--tone) 14%, transparent); }
.body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 6px; }
.head { display: flex; align-items: flex-start; gap: 8px; }
.title { flex: 1; font-size: 14px; font-weight: 600; line-height: 1.4; }
.x { flex: none; width: 26px; height: 26px; margin: -4px -4px 0 0; border: 0; border-radius: var(--r-control); background: transparent; color: var(--text-muted); cursor: pointer; font-size: 13px; }
.x:hover { background: var(--bg-hover); color: var(--text); }
.text { margin: 0; font-size: 12.5px; line-height: 1.5; color: var(--text-muted); overflow-wrap: anywhere; }
.acts { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 4px; }
.acts .btn { height: 30px; padding: 0 12px; font-size: 13px; }
@keyframes rise { from { opacity: 0; transform: translateY(6px); } to { opacity: 1; transform: none; } }
@media (prefers-reduced-motion: reduce) { .toast { animation: none; } }
</style>
