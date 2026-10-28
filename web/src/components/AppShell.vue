<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useWorkspace } from '../stores/workspace'
import { useChecks } from '../stores/checks'
import { useGit } from '../stores/git'
import { useRuns } from '../stores/runs'
import { useIssues } from '../stores/issues'
import BrandMark from './BrandMark.vue'
import ThemeToggle from './ThemeToggle.vue'
import { connection } from '../composables/events'
import NotificationBell from './NotificationBell.vue'
import NotificationToasts from './NotificationToasts.vue'
import { startNotifications, useNotifications } from '../stores/notifications'
import { startDelivery } from '../composables/notifyDelivery'
import DropMenu from './DropMenu.vue'
import { serviceApi } from '../api/client'
import type { ProjectView } from '../api/types'
import { projectId } from '../project'

const ws = useWorkspace()
const checks = useChecks()
const git = useGit()
const runs = useRuns()
const issues = useIssues()
const notes = useNotifications()
const unreadFailed = notes.unreadFailed
startNotifications()
startDelivery(useRouter())

const RANK: Record<string, number> = { dev: 0, test: 1, qa: 1, staging: 2, prod: 3 }
const envStatus = computed(() => new Map(runs.envs.map((e) => [e.name, e.status])))
const STATUS_TEXT: Record<string, string> = { pending: 'pending', failed: 'failed', running: 'running', drift: 'drift' }
// the first run waiting for approval in each environment: its row says "approve"
const waiting = computed(() => {
  const m = new Map<string, number>()
  for (const r of runs.list) if (r.status === 'waiting_approval' && !m.has(r.target.env)) m.set(r.target.env, r.id)
  return m
})
const envs = computed(() => {
  const cfg = ws.ws?.config
  const set = new Set<string>()
  for (const r of cfg?.terraform?.roots ?? []) {
    if (r.env) set.add(r.env)
    for (const k of Object.keys(r.envs ?? {})) set.add(k)
  }
  for (const p of cfg?.ansible?.projects ?? []) for (const k of Object.keys(p.inventories ?? {})) set.add(k)
  return [...set].sort((a, b) => (RANK[a] ?? 9) - (RANK[b] ?? 9) || a.localeCompare(b))
})
const repoLine = computed(() => {
  const s = git.status
  if (!s?.available) return 'no git'
  const n = s.files.length
  return `${s.branch ?? 'detached'} · ${n} uncommitted`
})
const port = location.port || '80'
const hasIaC = computed(() => (ws.ws?.config?.terraform?.roots ?? []).length + (ws.ws?.config?.ansible?.projects ?? []).length > 0)
// the project switcher lists the others, most recently run first; fetched on each open
const projects = ref<ProjectView[]>([])
async function loadProjects() {
  try { projects.value = await serviceApi<ProjectView[]>('/projects') } catch { /* All projects still works */ }
}
const SWITCH_MAX = 8
const otherProjects = computed(() => projects.value
  .filter((p) => p.id !== projectId && p.status === 'ok')
  .sort((a, b) => (b.last_run?.created_at ?? '').localeCompare(a.last_run?.created_at ?? '') || a.name.localeCompare(b.name))
  .slice(0, SWITCH_MAX))
const secretIssues = computed(() => checks.diagnostics.filter((d) => d.tool === 'secrets').length)
</script>

<template>
  <div class="app">
    <aside class="side" aria-label="Sidebar">
      <div class="between top">
        <a class="brand" href="/" title="All projects">
          <BrandMark />
          <span class="mono name">groundwork</span>
        </a>
        <NotificationBell />
      </div>

      <DropMenu class="switcher" button-class="repo" align="start" :label="`Switch project (current: ${ws.ws?.name ?? ''})`" @open="loadProjects">
        <template #button>
          <span class="rtext">
            <span class="between"><span class="lbl">Project</span><svg class="chev" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 9l4-4 4 4" /><path d="M8 15l4 4 4-4" /></svg></span>
            <span class="mono rname">{{ ws.ws?.name }}</span>
            <span class="mono rline">{{ repoLine }}</span>
          </span>
        </template>
        <a class="mono pitem" role="menuitemradio" aria-checked="true" :href="`/p/${projectId}/`">{{ ws.ws?.name }}</a>
        <a v-for="p in otherProjects" :key="p.id" class="mono pitem" role="menuitemradio" aria-checked="false" :href="`/p/${p.id}/`">{{ p.name }}</a>
        <div class="sep" role="separator" />
        <a class="all" role="menuitem" href="/">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" aria-hidden="true"><path d="M12 3l9 5-9 5-9-5z" /><path d="M3 13l9 5 9-5" /></svg>
          All projects
        </a>
      </DropMenu>

      <nav aria-label="Primary" class="nav-list" data-tour="nav">
        <RouterLink class="nav" to="/" exact-active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="3" width="7" height="7" rx="1" /><rect x="14" y="3" width="7" height="7" rx="1" /><rect x="3" y="14" width="7" height="7" rx="1" /><rect x="14" y="14" width="7" height="7" rx="1" /></svg>
          Overview
        </RouterLink>
        <RouterLink class="nav" to="/code" active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" aria-hidden="true"><path d="M4 4h6l2 2h8v14H4z" /></svg>
          Code
          <span v-if="checks.counts.error" class="mono badge fail" :aria-label="`${checks.counts.error} errors`">{{ checks.counts.error }}</span>
          <span v-else-if="checks.counts.warning" class="mono badge warn" :aria-label="`${checks.counts.warning} warnings`">{{ checks.counts.warning }}</span>
        </RouterLink>
        <RouterLink class="nav" to="/runs" active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" aria-hidden="true"><path d="M5 3l14 9-14 9z" /></svg>
          Runs
          <span v-if="unreadFailed" class="mono badge fail">{{ unreadFailed }} failed</span>
          <span v-else-if="runs.awaiting" class="mono badge warn" :aria-label="`${runs.awaiting} waiting for approval`">{{ runs.awaiting }} waiting</span>
          <span v-else-if="runs.live" class="mono badge run">{{ runs.live }} live</span>
        </RouterLink>
        <RouterLink v-if="hasIaC" class="nav" to="/variables" active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 4c-2 0-3 1-3 3v2c0 1.5-1 3-2 3 1 0 2 1.5 2 3v2c0 2 1 3 3 3" /><path d="M16 4c2 0 3 1 3 3v2c0 1.5 1 3 2 3-1 0-2 1.5-2 3v2c0 2-1 3-3 3" /></svg>
          Variables
          <span v-if="secretIssues" class="mono badge fail" :aria-label="`${secretIssues} plaintext secrets`">{{ secretIssues }}</span>
        </RouterLink>
        <RouterLink v-if="(ws.ws?.config?.ansible?.projects ?? []).length" class="nav" to="/inventory" active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><path d="M4 6h16M4 12h16M4 18h10" /></svg>
          Inventory
        </RouterLink>
        <RouterLink v-if="(ws.ws?.config?.terraform?.roots ?? []).length" class="nav" to="/graph" active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><circle cx="5" cy="12" r="2.5" /><circle cx="19" cy="5" r="2.5" /><circle cx="19" cy="19" r="2.5" /><path d="M7.3 11l9.4-5M7.3 13l9.4 5" /></svg>
          Graph
        </RouterLink>
        <RouterLink class="nav" to="/troubleshoot" active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3l9 16H3z" /><path d="M12 10v4M12 17h.01" /></svg>
          Troubleshoot
          <span v-if="issues.open.length" class="mono badge warn" :aria-label="`${issues.open.length} open issues`">{{ issues.open.length }}</span>
        </RouterLink>
        <RouterLink class="nav" to="/notifications" active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9" /><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0" /></svg>
          Notifications
        </RouterLink>
      </nav>

      <div v-if="envs.length" class="envs">
        <span class="lbl">Environments</span>
        <span v-for="e in envs" :key="e" class="nav static">
          <span class="dot" :class="waiting.has(e) ? 'pending' : envStatus.get(e) ?? 'unknown'" aria-hidden="true" />{{ e }}
          <RouterLink v-if="waiting.has(e)" class="mono badge warn approve" :to="`/runs/${waiting.get(e)}/plan`" :aria-label="`Review the plan waiting for approval on ${e}`">approve</RouterLink>
          <span v-else-if="STATUS_TEXT[envStatus.get(e) ?? '']" class="mono badge" :class="envStatus.get(e) === 'failed' ? 'fail' : envStatus.get(e) === 'pending' || envStatus.get(e) === 'drift' ? 'warn' : 'run'">{{ STATUS_TEXT[envStatus.get(e) ?? ''] }}</span>
        </span>
      </div>
      <div class="foot">
        <ThemeToggle />
        <span v-if="connection !== 'open'" class="mono conn" role="status" :title="connection === 'connecting' ? 'Connecting to the groundwork service' : 'The service is not answering; retrying. What you see may be out of date.'">
          <span class="dot" aria-hidden="true" />{{ connection === 'connecting' ? 'connecting…' : 'reconnecting…' }}
        </span>
        <span v-if="ws.ws?.version" class="mono ver" :title="`groundwork ${ws.ws.version}, port ${port}`">{{ /^\d/.test(ws.ws.version) ? 'v' + ws.ws.version : ws.ws.version }} · :{{ port }}</span>
      </div>
    </aside>
    <main class="main" :class="{ stale: connection === 'reconnecting' }"><slot /></main>
    <NotificationToasts />
  </div>
</template>

<style scoped>
.app { min-height: 100vh; display: flex; flex-wrap: wrap; }
.side { flex: 1 1 232px; max-width: 232px; padding: 20px 14px; border-right: 1px solid var(--line); display: flex; flex-direction: column; gap: 24px; background: var(--bg-sidebar);
  /* pinned, so it stays in view while long code or logs scroll the page */
  position: fixed; top: 0; bottom: 0; left: 0; width: 232px; overflow-y: auto; }
.main { flex: 999 1 560px; min-width: 0; display: flex; flex-direction: column; height: 100vh; margin-left: 232px; }
.brand { display: flex; align-items: center; gap: 10px; padding: 0 6px; color: inherit; text-decoration: none; }
.between { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.name { font-weight: 700; font-size: 14px; }
.ver { font-size: 13px; color: var(--text-label); word-break: break-all; }
.conn { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; color: var(--warn); padding: 3px 8px; border: 1px solid color-mix(in srgb, var(--warn) 40%, transparent); border-radius: 999px; }
.conn .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--warn); animation: pulse 1.2s ease-in-out infinite; }
@keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: .3; } }
.main.stale { opacity: .6; transition: opacity .3s; }
.switcher { display: flex; }
.switcher :deep(.repo) { all: unset; box-sizing: border-box; flex: 1; min-width: 0; display: flex; align-items: center; gap: 8px; padding: 10px 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--bg-card); cursor: pointer; }
.switcher :deep(.repo:hover), .switcher :deep(.repo[aria-expanded='true']) { border-color: var(--line-strong); }
.switcher :deep(.repo:focus-visible) { outline: 2px solid var(--accent); outline-offset: 2px; }
.switcher :deep(.menu) { min-width: 0; width: 100%; }
.rtext { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 6px; }
.chev { flex: none; color: var(--text-muted); }
.switcher .pitem { font-family: var(--font-mono); word-break: break-all; }
/* the slot content carries this component's scope, so these outrank the menu's item style */
.switcher .all { flex-direction: row; align-items: center; gap: 4px; padding-left: 8px; color: var(--accent); font-weight: 500; }
.rname { font-size: 14px; font-weight: 500; word-break: break-all; }
.rline { font-size: 13px; color: var(--text-muted); }
.foot { margin-top: auto; padding: 0 6px; display: flex; flex-direction: column; align-items: flex-start; gap: 10px; }
.nav-list { display: flex; flex-direction: column; gap: 2px; }
.nav { display: flex; align-items: center; gap: 10px; padding: 9px 12px; border-radius: 6px; color: var(--text-soft); text-decoration: none; font-size: 14px; }
.nav:hover:not(.static) { background: var(--bg-hover); color: var(--text); }
.nav.on { background: var(--bg-active); color: var(--text); box-shadow: inset 0 0 0 1px var(--line-strong); }
.nav.static { padding: 6px 12px; cursor: default; }
.badge { margin-left: auto; font-size: 13px; }
.envs { display: flex; flex-direction: column; gap: 4px; }
.envs .lbl { padding: 0 12px 4px; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--text-disabled); }
.dot.ok { background: var(--ok); } .dot.pending, .dot.drift { background: var(--warn); } .dot.failed { background: var(--fail); } .dot.running { background: var(--running); }
.badge.run { color: var(--running); }
.top { padding-right: 2px; }
.approve { text-decoration: none; padding: 1px 6px; border-radius: 4px; background: var(--warn-bg); border: 1px solid var(--warn-line); }
.approve:hover { color: var(--warn-strong); border-color: var(--warn); }
@media (max-width: 900px) { .side { max-width: 100%; flex-basis: 100%; position: static; width: auto; overflow: visible; } .main { height: auto; min-height: 70vh; margin-left: 0; } }
</style>
