<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from '../components/AppShell.vue'
import DropMenu from '../components/DropMenu.vue'
import InlineConfirm from '../components/InlineConfirm.vue'
import StatusPill from '../components/StatusPill.vue'
import TourOverlay, { type TourStep } from '../components/TourOverlay.vue'
import { api } from '../api/client'
import { useIssues } from '../stores/issues'
import { useWorkspace } from '../stores/workspace'
import { useChecks } from '../stores/checks'
import { useGit } from '../stores/git'
import { ago, duration, kindLabel, useRuns } from '../stores/runs'
import { useNow } from '../composables/now'
import type { DriftSchedule, EnvTarget, Onboarding } from '../api/types'
import { swallow } from '../api/swallow'

const router = useRouter()
const route = useRoute()
const issues = useIssues()
const ws = useWorkspace()
const checks = useChecks()
const git = useGit()
const runs = useRuns()
const now = useNow(5000)

const onboarding = ref<Onboarding | null>(null)
const schedule = ref<DriftSchedule | null>(null)
const touring = ref(false)
// The tour and checklist show on first use only: in the tab where groundwork
// first showed them, for that tab's session, and never again on this machine.
const FIRST_USE_TAB = 'gw.firstUse'
function firstUseTab(): boolean {
  try { return sessionStorage.getItem(FIRST_USE_TAB) === '1' } catch { return false }
}
const firstUse = ref(firstUseTab())
async function loadOnboarding() {
  const o = await api<Onboarding>('/onboarding')
  onboarding.value = o
  if (o.first_use) {
    firstUse.value = true
    try { sessionStorage.setItem(FIRST_USE_TAB, '1') } catch { /* this page view only, then */ }
  }
  if (route.query.tour === '1' || (o.first_use && o.tips && !o.tour_completed)) touring.value = true
}
onMounted(() => {
  checks.refresh(); git.refresh(); runs.refresh(); issues.refresh()
  loadOnboarding().catch(swallow('onboarding state'))
  api<DriftSchedule>('/drift/schedule').then((s) => (schedule.value = s)).catch(swallow('drift schedule'))
})
watch(() => route.query.tour, (t) => { if (t === '1') touring.value = true })
async function tourDone() {
  touring.value = false
  if (route.query.tour) router.replace({ query: {} })
  onboarding.value = await api<Onboarding>('/onboarding', { body: { tour_completed: true } })
}
async function hideChecklist() { onboarding.value = await api<Onboarding>('/onboarding', { body: { checklist_hidden: true } }) }
const doneCount = computed(() => onboarding.value?.items.filter((i) => i.done).length ?? 0)
const showChecklist = computed(() => !!onboarding.value && firstUse.value && !onboarding.value.checklist_hidden && doneCount.value < onboarding.value.items.length)
const nextStep = computed(() => onboarding.value?.items.find((i) => !i.done))
const allSteps = ref(false)
const openPalette = () => window.dispatchEvent(new Event('gw:palette'))

const counts = computed(() => {
  const c = ws.ws?.config
  const envs = new Set<string>()
  for (const r of c?.terraform?.roots ?? []) { if (r.env) envs.add(r.env); for (const k of Object.keys(r.envs ?? {})) envs.add(k) }
  for (const p of c?.ansible?.projects ?? []) for (const k of Object.keys(p.inventories ?? {})) envs.add(k)
  return { envs: envs.size, roots: (c?.terraform?.roots ?? []).length, projects: (c?.ansible?.projects ?? []).length }
})
const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`
const tourSteps = computed<TourStep[]>(() => [
  { title: 'Welcome to groundwork', body: `It scanned this repo and found ${plural(counts.value.envs, 'environment')}, ${plural(counts.value.roots, 'Terraform root')} and ${plural(counts.value.projects, 'Ansible project')}. Here is a one-minute tour of where things live.` },
  { title: 'Everything is one click away', body: 'Code is where you edit and visualize. Runs shows every plan, apply and playbook with live logs. Graph draws how resources connect. Troubleshoot explains errors.', anchor: 'nav' },
  { title: 'Each environment at a glance', body: 'See drift and pending changes per environment. Plan only previews changes. Nothing is applied until you review the plan and approve it.', anchor: 'envs' },
  { title: 'Checks run on every save', body: 'fmt, validate, tflint, ansible-lint, yamllint and the secrets scan re-run when a file changes, in the editor or in your own IDE.', anchor: 'checks' },
  { title: 'Problems come with a fix', body: 'Errors, stuck state locks and unreachable hosts land here with a suggested next step. Open Troubleshoot for the full walkthrough.', anchor: 'issues' },
  { title: 'Move fast with the keyboard', body: 'Search jumps to any file, run, host or environment, and starts plans. Reopen these tips any time with the ? button.', anchor: 'header', keys: true },
])
const SEV: Record<string, string> = { error: 'st-fail', warning: 'st-warn', info: 'st-idle' }
const fmtTime = (iso?: string) => (iso ? new Date(iso).toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' }) : '')

const error = ref('')
const starting = ref('')
async function plan(t: EnvTarget, destroy = false) {
  starting.value = t.root + t.env
  error.value = ''
  try {
    const r = await runs.plan(t.root, t.env, destroy)
    router.push('/runs/' + r.id)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally { starting.value = ''; destroying.value = '' }
}
// a destroy starts with a plan to review; this only confirms making that plan
const destroying = ref('')

const envPill: Record<string, [string, string]> = {
  ok: ['succeeded', 'in sync'], pending: ['waiting_approval', 'changes pending'], failed: ['failed', 'last run failed'],
  running: ['running', 'running'], unknown: ['queued', 'not planned yet'], drift: ['waiting_approval', 'drift'],
}
const pendingTotal = (t: EnvTarget) => (t.pending ? t.pending.create + t.pending.update + t.pending.replace + t.pending.delete : 0)
function lastLine(t: EnvTarget): string {
  const r = t.last_run
  if (!r) return 'never planned'
  const when = ago(r.created_at, now.value)
  return `${kindLabel(r).replace('terraform ', '')} #${r.id} ${r.status.replace('_', ' ')} ${when === 'now' ? 'just now' : when + ' ago'}`
}
const LAST: Record<string, string> = { failed: 'fail', cancelled: 'warn', waiting_approval: 'warn', running: 'run' }
const lastClass = (t: EnvTarget) => LAST[t.last_run?.status ?? ''] ?? ''
function hostLine(a: { hosts: number; down: number }): string {
  if (a.hosts < 0) return 'hosts unknown'
  const n = plural(a.hosts, 'host')
  return a.down ? `${n}, ${a.down} down` : a.hosts === 1 ? `${n}, up` : `${n}, all up`
}
const recent = computed(() => runs.list.slice(0, 6))
const allUnits = ['fmt', 'validate', 'tflint', 'checkov', 'yamllint', 'ansible-lint', 'syntax-check', 'secrets']
// only the tools some unit actually has, so the matrix fits its column
const units = computed(() => allUnits.filter((t) => checks.units.some((u) => u.results.some((r) => r.tool === t))))
const SHORT: Record<string, string> = { 'ansible-lint': 'a-lint', 'syntax-check': 'syntax' }
const checksRan = computed(() => checks.units.some((u) => u.results.some((r) => r.status !== 'idle')))
const hasTerraform = computed(() => (ws.ws?.config?.terraform?.roots ?? []).length > 0)
</script>

<template>
  <AppShell>
    <div class="page">
      <header class="topbar" data-tour="header">
        <div class="head">
          <h1>{{ ws.ws?.name }}</h1>
          <span class="mono muted small">{{ ws.ws?.mode }} repo</span>
        </div>
        <button class="search" type="button" aria-label="Search (⌘K)" @click="openPalette">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><circle cx="11" cy="11" r="7" /><path d="M20 20l-4-4" /></svg>
          <span>Jump to file, run, host, environment…</span><kbd class="mono">⌘K</kbd>
        </button>
        <button class="btn sm icon" type="button" aria-label="Show tips" @click="touring = true">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9" /><path d="M9.5 9.5a2.5 2.5 0 1 1 3.5 2.3c-.6.3-1 .9-1 1.6V14" /><path d="M12 17h.01" /></svg>
        </button>
      </header>

      <section v-if="showChecklist && onboarding" class="getting" aria-label="Getting started">
        <div class="gstrip">
          <span class="gk">Getting started · {{ doneCount }} of {{ onboarding.items.length }}</span>
          <div class="bar" role="img" :aria-label="`${doneCount} of ${onboarding.items.length} done`"><div :style="{ width: (100 * doneCount) / onboarding.items.length + '%' }" /></div>
          <span v-if="nextStep" class="gnext">Next: <b>{{ nextStep.label }}</b></span>
          <span class="gbtn">
            <RouterLink v-if="nextStep" class="btn sm" :to="nextStep.href">Open</RouterLink>
            <button class="btn sm ghost" type="button" :aria-expanded="allSteps" @click="allSteps = !allSteps">{{ allSteps ? 'Fewer' : 'All steps' }}</button>
            <button class="btn sm ghost" type="button" @click="hideChecklist">Hide</button>
          </span>
        </div>
        <div v-if="allSteps" class="gall">
          <ol class="gr">
            <li v-for="(it, k) in onboarding.items" :key="it.id" class="chk" :class="{ done: it.done }">
              <span class="tick" :aria-label="it.done ? 'done' : 'to do'">{{ it.done ? '✓' : k + 1 }}</span>
              <span>{{ it.label }}</span>
              <RouterLink v-if="!it.done" class="small" :to="it.href">Open →</RouterLink><span v-else />
            </li>
          </ol>
          <div class="gside"><span class="small muted">Nothing here changes your infrastructure until you approve an apply.</span><button class="btn sm" type="button" @click="touring = true">Replay tour</button></div>
        </div>
      </section>
      <p v-if="ws.ws?.config_error" class="err" role="alert">{{ ws.ws.config_error }}</p>
      <p v-for="w in ws.ws?.config_warnings ?? []" :key="w" class="warn" role="status">{{ w }}</p>
      <p v-if="error" class="err" role="alert">{{ error }}</p>

      <section class="attention" :class="{ quiet: !issues.open.length }" aria-label="Needs attention" data-tour="issues">
        <template v-if="issues.open.length">
          <div class="between ph"><h2>Needs attention · {{ issues.open.length }}</h2><RouterLink class="small" to="/troubleshoot">Troubleshoot →</RouterLink></div>
          <RouterLink v-for="i in issues.open.slice(0, 5)" :key="i.id" class="att" :class="i.severity" :to="'/troubleshoot/' + i.id">
            <span class="pill" :class="SEV[i.severity] ?? 'st-idle'">{{ i.category }}</span>
            <span class="abody"><span class="atitle">{{ i.title }}</span><span class="small amuted"><template v-if="i.steps[0]">Fix: {{ i.steps[0].title }}</template><template v-else>{{ i.target }}</template></span></span>
            <span class="btn sm">Fix it →</span>
          </RouterLink>
          <p v-if="issues.open.length > 5" class="pad small"><RouterLink to="/troubleshoot">{{ issues.open.length - 5 }} more →</RouterLink></p>
        </template>
        <p v-else class="small muted">Nothing needs attention. Failed runs and findings with a known cause show up here with a fix.</p>
      </section>

      <section v-if="runs.envs.length" class="envs" aria-label="Environments" data-tour="envs">
        <article v-for="e in runs.envs" :key="e.name" class="card env">
          <div class="between ehead">
            <span class="mono ename">{{ e.name }}</span>
            <StatusPill :status="envPill[e.status][0]" :label="envPill[e.status][1]" />
          </div>
          <div v-for="t in e.targets" :key="t.root" class="target">
            <span class="lbl">Terraform</span>
            <div class="tinfo">
              <span class="mono path">{{ t.root }}</span>
              <span class="small muted"><span :class="lastClass(t)">{{ lastLine(t) }}</span>
                · <template v-if="t.pending"><RouterLink :to="'/runs/' + t.pending.run_id + '/plan'">{{ pendingTotal(t) }} {{ pendingTotal(t) === 1 ? 'change' : 'changes' }} to review</RouterLink> · </template>
                <span :class="{ warn: t.approval_required }">{{ t.approval_required ? 'needs approval to apply' : 'applies without approval' }}</span>
                · <RouterLink v-if="t.drift" class="warn" :to="{ path: '/graph', query: { root: t.root, env: t.env } }">{{ t.drift }} drifted</RouterLink><template v-else>no drift</template></span>
            </div>
            <span class="acts">
              <DropMenu :label="`More actions for ${e.name}`" align="end">
                <template #button><span aria-hidden="true">⋯</span></template>
                <button class="mi dg" type="button" role="menuitem" :disabled="t.running || starting === t.root + t.env" @click="destroying = t.root + t.env">Destroy…</button>
              </DropMenu>
              <RouterLink v-if="t.pending" class="btn sm primary" :to="'/runs/' + t.pending.run_id + '/plan'">Review &amp; approve</RouterLink>
              <button v-else class="btn sm" :class="{ primary: t.last_run?.status === 'failed' }" type="button" :disabled="t.running || starting === t.root + t.env" @click="plan(t)">{{ t.running ? 'Running…' : 'Plan' }}</button>
            </span>
            <InlineConfirm v-if="destroying === t.root + t.env" danger class="dconf" :message="`Plan the destruction of everything in ${t.env}?`" confirm-label="Plan destroy"
              :busy="starting === t.root + t.env" @confirm="plan(t, true)" @cancel="destroying = ''">
              <p class="small muted">Nothing is destroyed yet. You review the plan, then type <span class="mono">{{ t.env }}</span> to approve it.</p>
            </InlineConfirm>
          </div>
          <div v-for="a in e.ansible" :key="'a' + a.project" class="target">
            <span class="lbl">Ansible</span>
            <div class="tinfo">
              <RouterLink class="mono path" :to="{ path: '/inventory', query: { project: a.project, env: e.name } }">{{ a.project === '.' ? 'project root' : a.project }}</RouterLink>
              <span class="small muted"><span :class="{ fail: a.down }">{{ hostLine(a) }}</span> · {{ a.last_run ? `${kindLabel(a.last_run)} #${a.last_run.id} ${a.last_run.status.replace('_', ' ')}` : 'no playbook runs yet' }}</span>
            </div>
            <span class="acts"><button class="btn sm" type="button" @click="router.push({ path: '/inventory', query: { project: a.project, env: e.name, run: 'playbook' } })">Run playbook</button></span>
          </div>
        </article>
      </section>
      <p v-else-if="!hasTerraform" class="muted">No environments are configured yet.</p>
      <p v-if="schedule?.schedule" class="small muted">Drift checks run on schedule <span class="mono">{{ schedule.schedule }}</span><template v-if="schedule.next"> · next {{ fmtTime(schedule.next) }}</template><template v-if="schedule.notify"> · notify: {{ schedule.notify }}</template><span v-if="schedule.error" class="fail"> · {{ schedule.error }}</span></p>

      <div class="grid2">
        <section class="card flush" aria-label="Recent runs">
          <div class="between ph"><h2>Recent runs</h2><RouterLink class="small" to="/runs">All runs →</RouterLink></div>
          <div class="scroll">
            <RouterLink v-for="r in recent" :key="r.id" :to="'/runs/' + r.id" class="row">
              <span class="mono small muted">#{{ r.id }}</span>
              <span class="rt"><span class="mono">{{ kindLabel(r) }}</span><span class="small muted">{{ r.target.env }} · {{ ago(r.created_at, now) }}</span></span>
              <StatusPill :status="r.status" />
              <span class="mono small muted dur">{{ duration(r.started_at ?? r.created_at, r.ended_at, now) }}</span>
            </RouterLink>
            <p v-if="!recent.length" class="muted pad">No runs yet. Press Plan on an environment above.</p>
          </div>
        </section>

        <section class="card flush" aria-label="Checks" data-tour="checks">
          <div class="between ph">
            <h2>Checks <span class="mono sums">
              <template v-if="!checksRan"><span class="muted">not run yet</span></template>
              <template v-else><span :class="checks.counts.error ? 'fail' : 'ok'">{{ checks.counts.error }} errors</span> · <span :class="checks.counts.warning ? 'warn' : 'ok'">{{ checks.counts.warning }} warnings</span></template>
              <template v-if="git.status?.available"> · {{ git.dirty }} uncommitted</template></span></h2>
            <button class="btn sm" type="button" :disabled="checks.running" @click="checks.run('', true)">{{ checks.running ? 'Checking…' : 'Run all checks' }}</button>
          </div>
          <div class="scroll">
            <table v-if="checks.units.length" class="mono">
              <thead><tr><th>unit</th><th v-for="t in units" :key="t" :title="t">{{ SHORT[t] ?? t }}</th></tr></thead>
              <tbody>
                <tr v-for="u in checks.units" :key="u.id">
                  <td class="b"><RouterLink :to="'/code/' + u.path.split('/').map(encodeURIComponent).join('/')">{{ u.path }}</RouterLink></td>
                  <td v-for="t in units" :key="t">
                    <template v-for="r in u.results.filter((x) => x.tool === t)" :key="r.tool">
                      <span :class="r.status" :title="[r.status === 'idle' ? 'not run yet' : '', r.message, r.hint].filter(Boolean).join(' — ')">{{ r.status === 'issues' ? r.count : r.status === 'idle' ? '–' : r.status }}</span>
                    </template>
                    <span v-if="!u.results.some((x) => x.tool === t)" class="muted" title="does not apply">·</span>
                  </td>
                </tr>
              </tbody>
            </table>
            <p v-else class="muted pad">No units to check yet.</p>
          </div>
        </section>
      </div>
    </div>
    <TourOverlay v-if="touring" :steps="tourSteps" @done="tourDone" />
  </AppShell>
</template>

<style scoped>
.page { padding: 24px 32px; display: flex; flex-direction: column; gap: 20px; overflow: auto; }
.topbar { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
.head { display: flex; align-items: baseline; gap: 12px; }
.search { flex: 1 1 260px; max-width: 440px; margin-left: auto; display: flex; align-items: center; gap: 8px; height: 36px; padding: 0 12px; border: 1px solid var(--line-strong); border-radius: 6px; background: var(--bg-card); color: var(--text-label); font: inherit; font-size: 14px; cursor: text; text-align: left; }
.search span { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.search kbd { font-size: 12px; border: 1px solid var(--line-strong); border-radius: 4px; padding: 1px 5px; }
.search:focus-visible { outline: 2px solid var(--accent); }
.icon { width: 36px; padding: 0; justify-content: center; }
.btn.ghost { border-color: transparent; background: transparent; color: var(--text-muted); }
.getting { border: 1px solid var(--accent-line); border-radius: 10px; background: var(--accent-bg); }
.gstrip { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 18px; padding: 10px 16px; }
.gk { font-size: 12.5px; font-weight: 600; color: var(--accent); text-transform: uppercase; letter-spacing: .08em; white-space: nowrap; }
.gnext { font-size: 14px; flex: 1 1 240px; min-width: 0; }
.gnext b { font-weight: 600; }
.bar { width: 140px; height: 6px; border-radius: 3px; background: var(--line); overflow: hidden; }
.bar div { height: 100%; background: var(--accent); }
.gbtn { display: flex; gap: 6px; margin-left: auto; flex-wrap: wrap; }
.gall { display: flex; flex-wrap: wrap; gap: 12px 32px; padding: 4px 16px 14px; border-top: 1px solid var(--accent-line); }
.gside { flex: 1 1 220px; display: flex; flex-direction: column; align-items: flex-start; gap: 10px; padding-top: 10px; }
.gr { flex: 2 1 420px; list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; }
.chk { display: grid; grid-template-columns: 24px minmax(0, 1fr) auto; gap: 12px; align-items: center; padding: 9px 0; border-top: 1px solid var(--accent-line); font-size: 14.5px; }
.chk:first-child { border-top: 0; }
.chk.done > span:nth-child(2) { color: var(--text-muted); }
.tick { width: 22px; height: 22px; border-radius: 50%; display: grid; place-items: center; font: 600 12px var(--font-mono); border: 1px solid var(--accent-line); color: var(--text-muted); }
.chk.done .tick { background: var(--accent); border-color: var(--accent); color: var(--on-accent); }
.attention { border: 1px solid var(--attn-line); border-radius: 10px; background: var(--attn-bg); overflow: hidden; }
.attention.quiet { padding: 10px 16px; background: var(--bg-panel); border-color: var(--line); }
.attention.quiet p { margin: 0; }
.attention .ph { border-bottom-color: var(--attn-line); }
.att { display: flex; align-items: center; gap: 14px; padding: 12px 18px; border-bottom: 1px solid var(--attn-line); color: inherit; text-decoration: none; }
.att:last-child { border-bottom: 0; }
.att.error { background: var(--fail-bg); }
.att:hover { background: var(--attn-hover); }
.abody { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.atitle { font-size: 14.5px; font-weight: 600; color: var(--text); }
.amuted { color: var(--text-soft); }
.pill { font: 600 12.5px var(--font-mono); padding: 3px 8px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; }
.st-fail { color: var(--fail); background: color-mix(in srgb, var(--fail) 10%, transparent); } .st-warn { color: var(--warn); background: color-mix(in srgb, var(--warn) 10%, transparent); } .st-idle { color: var(--text-muted); background: color-mix(in srgb, var(--text-muted) 10%, transparent); }
.fail { color: var(--fail); }
h1 { margin: 0; font-size: 28px; font-weight: 600; letter-spacing: -.01em; }
h2 { margin: 0; font-size: 14px; font-weight: 600; }
.small { font-size: 13px; } .small2 { font-size: 14px; }
.between { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.envs { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(420px, 100%), 1fr)); gap: 16px; }
.env { padding: 0; gap: 0; border-radius: var(--r-card); background: var(--bg-panel); overflow: hidden; }
.ehead { padding: 14px 18px; border-bottom: 1px solid var(--line); }
.ename { font-size: 16px; font-weight: 700; }
.target { display: grid; grid-template-columns: 88px minmax(0, 1fr) auto; gap: 8px 14px; align-items: center; padding: 14px 18px; border-bottom: 1px solid var(--line); }
.target:last-child { border-bottom: 0; }
.tinfo { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.path { font-size: 13.5px; color: var(--text); overflow-wrap: anywhere; text-decoration: none; }
a.path:hover { text-decoration: underline; }
.tinfo .warn, .tinfo a.warn { color: var(--warn); }
.tinfo .run { color: var(--running); }
.lbl { font-size: 12.5px; font-weight: 600; color: var(--text-label); text-transform: uppercase; letter-spacing: .06em; }
.acts { display: flex; align-items: center; gap: 8px; }
.acts .mi.dg:not([disabled]) { color: var(--fail); }
/* no checkmark gutter: this menu has nothing to tick */
.acts .mi.dg { padding: 10px 14px; }
.dconf { grid-column: 1 / -1; margin-top: 6px; }
.dconf p { margin: 0; line-height: 1.55; }
.dconf :deep(.ic) { gap: 14px; padding: 16px 18px; }
.dconf :deep(.row) { gap: 10px; margin-top: 4px; }
.dconf :deep(.row .btn) { padding-inline: 16px; min-height: 36px; }
.grid2 { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.6fr); gap: 16px; align-items: start; }
@media (max-width: 1100px) { .grid2 { grid-template-columns: minmax(0, 1fr); } }
.card.flush { padding: 0; gap: 0; border-radius: var(--r-card); background: var(--bg-panel); overflow: hidden; }
.ph { padding: 14px 18px; border-bottom: 1px solid var(--line); }
.scroll { overflow-x: auto; }
.row { display: grid; grid-template-columns: 40px minmax(0, 1fr) auto 48px; gap: 10px; align-items: center; padding: 11px 18px; border-bottom: 1px solid var(--bg-hover); color: inherit; text-decoration: none; }
.row:hover { background: var(--bg-raised); }
.rt { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.dur { text-align: right; }
.sums { font-size: 13px; font-weight: 400; margin-left: 6px; }
.pad { padding: 14px 18px; margin: 0; }
table { width: 100%; border-collapse: collapse; font-size: 13.5px; }
th { text-align: left; color: var(--text-label); font-weight: 500; padding: 6px 6px; border-bottom: 1px solid var(--line); white-space: nowrap; }
td { padding: 8px 6px; border-bottom: 1px solid var(--line); white-space: nowrap; }
th:first-child, td:first-child { padding-left: 18px; }
th:last-child, td:last-child { padding-right: 18px; }
.b { font-weight: 700; }
td .ok { color: var(--ok); } td .issues { color: var(--warn); } td .failed { color: var(--fail); }
td .running { color: var(--running); } td .idle, td .skipped { color: var(--text-label); }
@media (max-width: 640px) { .page { padding: 20px 16px; } }
</style>
