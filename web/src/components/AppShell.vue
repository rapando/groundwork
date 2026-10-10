<script setup lang="ts">
import { computed } from 'vue'
import { useWorkspace } from '../stores/workspace'
import { useChecks } from '../stores/checks'
import { useGit } from '../stores/git'
import { useRuns } from '../stores/runs'
import { useIssues } from '../stores/issues'
import BrandMark from './BrandMark.vue'
import ThemeToggle from './ThemeToggle.vue'

const ws = useWorkspace()
const checks = useChecks()
const git = useGit()
const runs = useRuns()
const issues = useIssues()

const RANK: Record<string, number> = { dev: 0, test: 1, qa: 1, staging: 2, prod: 3 }
const envStatus = computed(() => new Map(runs.envs.map((e) => [e.name, e.status])))
const STATUS_TEXT: Record<string, string> = { pending: 'pending', failed: 'failed', running: 'running', drift: 'drift' }
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
const secretIssues = computed(() => checks.diagnostics.filter((d) => d.tool === 'secrets').length)
</script>

<template>
  <div class="app">
    <aside class="side" aria-label="Sidebar">
      <a class="brand" href="/" title="All projects">
        <BrandMark />
        <div class="col"><span class="mono name">groundwork</span><span class="mono ver">{{ ws.ws?.version }} · :{{ port }}</span></div>
      </a>

      <div class="repo">
        <div class="between"><span class="lbl">Project</span><a class="switch" href="/">All projects</a></div>
        <span class="mono rname">{{ ws.ws?.name }}</span>
        <span class="mono rline">{{ repoLine }}</span>
      </div>

      <nav aria-label="Primary" class="nav-list" data-tour="nav">
        <RouterLink class="nav" to="/" exact-active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="3" width="7" height="7" rx="1" /><rect x="14" y="3" width="7" height="7" rx="1" /><rect x="3" y="14" width="7" height="7" rx="1" /><rect x="14" y="14" width="7" height="7" rx="1" /></svg>
          Overview
        </RouterLink>
        <RouterLink class="nav" to="/runs" active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" aria-hidden="true"><path d="M5 3l14 9-14 9z" /></svg>
          Runs
          <span v-if="runs.live" class="mono badge run">{{ runs.live }} live</span>
          <span v-else-if="runs.awaiting" class="mono badge warn" :aria-label="`${runs.awaiting} awaiting approval`">{{ runs.awaiting }}</span>
        </RouterLink>
        <RouterLink v-if="(ws.ws?.config?.terraform?.roots ?? []).length" class="nav" to="/graph" active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><circle cx="5" cy="12" r="2.5" /><circle cx="19" cy="5" r="2.5" /><circle cx="19" cy="19" r="2.5" /><path d="M7.3 11l9.4-5M7.3 13l9.4 5" /></svg>
          Graph
        </RouterLink>
        <RouterLink v-if="(ws.ws?.config?.ansible?.projects ?? []).length" class="nav" to="/inventory" active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><path d="M4 6h16M4 12h16M4 18h10" /></svg>
          Inventory
        </RouterLink>
        <RouterLink v-if="hasIaC" class="nav" to="/variables" active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 4c-2 0-3 1-3 3v2c0 1.5-1 3-2 3 1 0 2 1.5 2 3v2c0 2 1 3 3 3" /><path d="M16 4c2 0 3 1 3 3v2c0 1.5 1 3 2 3-1 0-2 1.5-2 3v2c0 2-1 3-3 3" /></svg>
          Variables
          <span v-if="secretIssues" class="mono badge fail" :aria-label="`${secretIssues} plaintext secrets`">{{ secretIssues }}</span>
        </RouterLink>
        <RouterLink class="nav" to="/troubleshoot" active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3l9 16H3z" /><path d="M12 10v4M12 17h.01" /></svg>
          Troubleshoot
          <span v-if="issues.open.length" class="mono badge warn" :aria-label="`${issues.open.length} open issues`">{{ issues.open.length }}</span>
        </RouterLink>
        <RouterLink class="nav" to="/code" active-class="on">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" aria-hidden="true"><path d="M4 4h6l2 2h8v14H4z" /></svg>
          Code
          <span v-if="checks.counts.error" class="mono badge fail" :aria-label="`${checks.counts.error} errors`">{{ checks.counts.error }}</span>
          <span v-else-if="checks.counts.warning" class="mono badge warn" :aria-label="`${checks.counts.warning} warnings`">{{ checks.counts.warning }}</span>
        </RouterLink>
      </nav>

      <div v-if="envs.length" class="envs">
        <span class="lbl">Environments</span>
        <span v-for="e in envs" :key="e" class="nav static">
          <span class="dot" :class="envStatus.get(e) ?? 'unknown'" aria-hidden="true" />{{ e }}
          <span v-if="STATUS_TEXT[envStatus.get(e) ?? '']" class="mono badge" :class="envStatus.get(e) === 'failed' ? 'fail' : envStatus.get(e) === 'pending' || envStatus.get(e) === 'drift' ? 'warn' : 'run'">{{ STATUS_TEXT[envStatus.get(e) ?? ''] }}</span>
        </span>
      </div>
      <div class="foot"><ThemeToggle /></div>
    </aside>
    <main class="main"><slot /></main>
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
.switch { font-size: 14px; text-decoration: none; }
.switch:hover { text-decoration: underline; }
.col { display: flex; flex-direction: column; }
.name { font-weight: 700; font-size: 14px; }
.ver { font-size: 12px; color: var(--text-label); word-break: break-all; }
.repo { display: flex; flex-direction: column; gap: 6px; padding: 10px 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--bg-card); }
.rname { font-size: 14px; font-weight: 500; word-break: break-all; }
.rline { font-size: 13px; color: var(--text-muted); }
.foot { margin-top: auto; padding: 0 6px; }
.nav-list { display: flex; flex-direction: column; gap: 2px; }
.nav { display: flex; align-items: center; gap: 10px; padding: 9px 12px; border-radius: 6px; color: var(--text-soft); text-decoration: none; font-size: 14px; }
.nav:hover:not(.static) { background: var(--bg-hover); color: var(--text); }
.nav.on { background: var(--bg-active); color: var(--text); box-shadow: inset 0 0 0 1px var(--line-strong); }
.nav.static { padding: 6px 12px; cursor: default; }
.badge { margin-left: auto; font-size: 12px; }
.envs { display: flex; flex-direction: column; gap: 4px; }
.envs .lbl { padding: 0 12px 4px; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--text-disabled); }
.dot.ok { background: var(--ok); } .dot.pending, .dot.drift { background: var(--warn); } .dot.failed { background: var(--fail); } .dot.running { background: var(--running); }
.badge.run { color: var(--running); }
@media (max-width: 900px) { .side { max-width: 100%; flex-basis: 100%; position: static; width: auto; overflow: visible; } .main { height: auto; min-height: 70vh; margin-left: 0; } }
</style>
