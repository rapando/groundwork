<script setup lang="ts">
// Notification settings. In a project: two scopes, its own overrides
// (groundwork.yaml) and the settings for every project (the service's
// notify.yaml). On the project list: every project's only.
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from '../components/AppShell.vue'
import NotifySettingsForm from '../components/NotifySettingsForm.vue'
import BrandMark from '../components/BrandMark.vue'
import NotificationBell from '../components/NotificationBell.vue'
import ThemeToggle from '../components/ThemeToggle.vue'
import type { NotifySettingsResponse } from '../api/types'
import { projectId } from '../project'
import { useWorkspace } from '../stores/workspace'
import { desktopPermission, desktopSupported, requestDesktop } from '../composables/notifyDelivery'
import { startNotifications } from '../stores/notifications'

const ws = useWorkspace()
// on the project list there is no AppShell to start the bell
if (!projectId) startNotifications()
const route = useRoute()
const router = useRouter()
const scope = computed<'all' | 'project'>(() => (!projectId || route.query.scope === 'all' ? 'all' : 'project'))
const shared = ref(true)
const configured = ref(false)
const path = ref('')
function choose(s: 'all' | 'project') {
  router.replace({ query: s === 'all' ? { scope: 'all' } : {} })
}
function loaded(r: NotifySettingsResponse) {
  configured.value = r.configured
  if (scope.value === 'project') shared.value = r.shared !== false
  else path.value = r.path ?? ''
}
watch(scope, () => (configured.value = false))

const RANK: Record<string, number> = { dev: 0, test: 1, qa: 1, staging: 2, prod: 3 }
const envNames = computed(() => {
  const cfg = ws.ws?.config
  const set = new Set<string>()
  for (const r of cfg?.terraform?.roots ?? []) {
    if (r.env) set.add(r.env)
    for (const k of Object.keys(r.envs ?? {})) set.add(k)
  }
  for (const p of cfg?.ansible?.projects ?? []) for (const k of Object.keys(p.inventories ?? {})) set.add(k)
  return [...set].sort((a, b) => (RANK[a] ?? 9) - (RANK[b] ?? 9) || a.localeCompare(b))
})
const projectName = computed(() => ws.ws?.name || projectId)

onMounted(() => {
  document.title = 'Notifications · groundwork'
  if (desktopSupported()) desktopPermission.value = Notification.permission
})
</script>

<template>
  <component :is="projectId ? AppShell : 'div'" :class="{ shell: !projectId }">
    <header v-if="!projectId" class="top">
      <BrandMark />
      <a class="mono name" href="/">groundwork</a>
      <span class="spacer" />
      <NotificationBell align="right" />
      <ThemeToggle />
    </header>
    <div class="page" :class="{ solo: !projectId }">
      <header class="phead">
        <a v-if="!projectId" class="back" href="/">← Projects</a>
        <h1>Notifications</h1>
        <div v-if="projectId && shared" class="seg" role="radiogroup" aria-label="Settings for">
          <button type="button" role="radio" :aria-checked="scope === 'all'" :class="{ on: scope === 'all' }" @click="choose('all')">All projects</button>
          <button type="button" role="radio" :aria-checked="scope === 'project'" :class="{ on: scope === 'project' }" @click="choose('project')">This project<span v-if="projectName" class="mono"> · {{ projectName }}</span></button>
        </div>
        <p v-if="scope === 'project'" class="muted">
          Overrides go in <span class="mono">groundwork.yaml</span> under <span class="mono">notify</span>.
          <template v-if="shared">Anything you leave alone follows <b>All projects</b>.</template>
          The console always lists every event.
        </p>
        <p v-else class="muted">
          Every project starts from these, and the service keeps them<template v-if="path"> in <span class="mono">{{ path }}</span></template>, not in any repository.
          A project's <span class="mono">notify:</span> block overrides them key by key.
        </p>
        <p v-if="scope === 'all' && !configured" class="muted small">These are the defaults. Saving keeps them for every project so you can change them.</p>
      </header>

      <NotifySettingsForm :key="scope" :scope="scope" :env-names="scope === 'project' ? envNames : []" @loaded="loaded" />

      <section class="card browser" aria-labelledby="browser-title">
        <h2 id="browser-title">This browser</h2>
        <template v-if="desktopPermission === 'granted'">
          <p class="muted"><span class="ok">✓</span> Shows desktop notifications while a console tab is open. A click opens the run.</p>
        </template>
        <template v-else-if="desktopPermission === 'default'">
          <p class="muted">Allow notifications so a click on one opens the run here. Until then the service sends them itself.</p>
          <div><button class="btn" type="button" @click="requestDesktop()">Allow desktop notifications in this browser</button></div>
        </template>
        <p v-else-if="desktopPermission === 'denied'" class="muted">This browser blocks notifications from groundwork. Allow them in the site settings next to the address bar, then reload the page.</p>
        <p v-else class="muted">This browser can't show desktop notifications. The service sends them itself when no console is open.</p>
      </section>
    </div>
  </component>
</template>

<style scoped>
.shell { min-height: 100vh; }
.top { display: flex; align-items: center; gap: 12px; padding: 16px 32px; border-bottom: 1px solid var(--line); }
.top .name { font-weight: 700; color: var(--text); text-decoration: none; }
.spacer { flex: 1; }
.page { padding: 24px 32px; display: flex; flex-direction: column; gap: 20px; overflow: auto; max-width: 960px; }
.page.solo { margin: 0 auto; padding-top: 32px; }
.phead { display: flex; flex-direction: column; gap: 8px; align-items: flex-start; }
.back { font-size: 14px; color: var(--text-muted); }
h1 { margin: 0; font-size: 22px; font-weight: 600; }
h2 { margin: 0; font-size: 16px; font-weight: 600; }
.phead p, .browser p { margin: 0; font-size: 14.5px; }
.small { font-size: 13.5px; }
.seg { display: inline-flex; border: 1px solid var(--line-strong); border-radius: var(--r-control); padding: 2px; background: var(--bg-panel); }
.seg button { border: 0; background: none; padding: 6px 12px; border-radius: calc(var(--r-control) - 2px); font: inherit; font-size: 14px; color: var(--text-muted); cursor: pointer; }
.seg button:hover { color: var(--text); }
.seg button.on { background: var(--bg-active); color: var(--text); font-weight: 500; }
.browser { gap: 10px; }
@media (max-width: 640px) { .page { padding: 20px 16px; } .top { padding: 12px 16px; } }
</style>
