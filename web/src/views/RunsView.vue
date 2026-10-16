<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from '../components/AppShell.vue'
import StatusPill from '../components/StatusPill.vue'
import StagePipeline from '../components/StagePipeline.vue'
import LogView from '../components/LogView.vue'
import RunResourceMap from '../components/RunResourceMap.vue'
import RunHostGrid from '../components/RunHostGrid.vue'
import { api, ApiError } from '../api/client'
import { apiBase } from '../project'
import type { HostResult, PlanChange, ResEvent, RunDetail } from '../api/types'
import { ago, duration, kindLabel, useRuns } from '../stores/runs'
import { useNow } from '../composables/now'
import { onEvent } from '../composables/events'
import { useRunLog } from '../composables/runLog'

const route = useRoute()
const router = useRouter()
const runs = useRuns()
const now = useNow()

const id = computed(() => (route.params.id ? Number(route.params.id) : null))
const detail = ref<RunDetail | null>(null)
const loadError = ref('')
const filter = ref<'all' | 'active' | 'approval' | 'failed'>('all')
const notice = ref('')

// ---- list ----
const shown = computed(() => runs.list.filter((r) => {
  switch (filter.value) {
    case 'active': return r.status === 'running' || r.status === 'queued'
    case 'approval': return r.status === 'waiting_approval'
    case 'failed': return r.status === 'failed'
    default: return true
  }
}))
const dotClass = (s: string) => ({ succeeded: 'd-ok', running: 'd-run', failed: 'd-fail', waiting_approval: 'd-warn' }[s] ?? 'd-idle')

// ---- detail ----
async function loadDetail() {
  if (id.value == null) { detail.value = null; return }
  try {
    detail.value = await api<RunDetail>(`/runs/${id.value}`)
    loadError.value = ''
  } catch (e) {
    detail.value = null
    loadError.value = e instanceof ApiError && e.status === 404 ? 'No such run.' : String(e)
  }
}
watch(id, () => { resMap.clear(); results.value = []; follow.value = true; query.value = ''; loadResources(); loadResults(); loadDetail() }, { immediate: false })
const offs = [
  onEvent('run.updated', (d: { id: number }) => { if (d.id === id.value) loadDetail() }),
  onEvent('reconnected', () => loadDetail()),
]
onBeforeUnmount(() => offs.forEach((off) => off()))

onMounted(async () => {
  try {
    await runs.refresh()
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
    return
  }
  if (id.value == null && runs.list.length) router.replace('/runs/' + runs.list[0].id)
  loadDetail()
  loadResources()
  loadResults()
})

// ---- per-resource progress ----
const resMap = reactive(new Map<string, ResEvent>())
async function loadResources() {
  if (id.value == null) return
  const r = await api<{ resources: ResEvent[] }>(`/runs/${id.value}/resources`)
  if (id.value != null) r.resources.forEach((x) => resMap.set(x.address, x))
}

// ---- ansible ----
const isAnsible = computed(() => detail.value?.target.tool === 'ansible')
const results = ref<HostResult[]>([])
const phase = ref<'check' | 'run'>('check')
async function loadResults() {
  if (id.value == null) return
  try {
    const r = await api<{ results: HostResult[] }>(`/runs/${id.value}/hosts`)
    results.value = r.results
    if (r.results.some((x) => x.phase === 'run')) phase.value = 'run'
  } catch { results.value = [] }
}
offs.push(onEvent('ans.result', (d: { run: number; result: HostResult }) => {
  if (d.run !== id.value) return
  results.value = results.value.concat(d.result)
  if (d.result.phase === 'run') phase.value = 'run'
}))
const RANK: Record<string, number> = { skipped: 0, ok: 1, changed: 2, failed: 3, unreachable: 4 }
const hostRows = computed(() => {
  const by = new Map<string, { host: string; worst: string; ok: number; changed: number; failed: number; skipped: number; unreachable: number; tasks: HostResult[] }>()
  for (const r of results.value.filter((x) => x.phase === phase.value)) {
    const h = by.get(r.host) ?? { host: r.host, worst: 'skipped', ok: 0, changed: 0, failed: 0, skipped: 0, unreachable: 0, tasks: [] }
    h.tasks.push(r)
    if (r.status === 'failed' && r.ignored) h.ok++
    else if (r.status === 'failed') h.failed++
    else h[r.status]++
    if (!(r.status === 'failed' && r.ignored) && RANK[r.status] > RANK[h.worst]) h.worst = r.status
    by.set(r.host, h)
  }
  return [...by.values()].sort((a, b) => RANK[b.worst] - RANK[a.worst] || a.host.localeCompare(b.host))
})
const hasPhase = (p: string) => results.value.some((x) => x.phase === p)
const phaseResults = computed(() => results.value.filter((x) => x.phase === phase.value))

// inline approval for playbooks (the dry run is the "plan")
const typed = ref('')
const approving = ref(false)
const approveError = ref('')
async function approveAnsible() {
  if (!run.value) return
  approving.value = true
  approveError.value = ''
  try {
    await runs.approve(run.value.id, typed.value.trim())
    await loadDetail()
  } catch (e) {
    approveError.value = e instanceof ApiError && e.code === 'stale_plan'
      ? 'Re-run the dry run: ' + ((e.details as { reasons: string[] }).reasons ?? []).join('; ')
      : e instanceof Error ? e.message : String(e)
  } finally { approving.value = false }
}
async function rerunCheck() {
  if (!run.value) return
  const r = await api<{ run: { id: number } }>(`/runs/${run.value.id}/replan`, { method: 'POST', body: {} })
  router.push('/runs/' + r.run.id)
}

// ---- log ----
const level = ref('')
const query = ref('')
const follow = ref(true)
const wrap = ref(false)
const log = useRunLog(id, level, query, (r) => resMap.set(r.address, r))
const warnCount = computed(() => log.lines.value.filter((l) => l.level === 'warn').length)
const errCount = computed(() => log.lines.value.filter((l) => l.level === 'error').length)

// ---- derived ----
const run = computed(() => detail.value?.run)
const plan = computed(() => detail.value?.summary.plan)
const active = computed(() => run.value && ['queued', 'running', 'waiting_approval'].includes(run.value.status))
function stateOf(c: PlanChange): { text: string; cls: string } {
  const r = resMap.get(c.address)
  if (r?.state === 'done') return { text: 'done', cls: 'ok' }
  if (r?.state === 'failed') return { text: 'failed', cls: 'fail' }
  if (r?.state === 'running') return { text: r.elapsed ? `running ${r.elapsed}s` : 'running', cls: 'run' }
  if (run.value?.status === 'succeeded' && detail.value?.summary.apply) return { text: 'done', cls: 'ok' }
  return { text: 'planned', cls: 'idle' }
}
const states = computed(() => Object.fromEntries((plan.value?.changes ?? []).map((c) => [c.address, stateOf(c)])))
const doneCount = computed(() => Object.values(states.value).filter((s) => s.cls === 'ok').length)

// ---- tabs: what changes (the default) and the log ----
type Tab = 'changes' | 'log'
// init has nothing to draw, so it opens on its log
const defaultTab = computed<Tab>(() => (run.value?.kind === 'tf.init' ? 'log' : 'changes'))
const tab = computed<Tab>({
  get: () => (route.query.tab === 'log' || route.query.tab === 'changes' ? route.query.tab : defaultTab.value),
  set: (v) => { router.replace({ query: { ...route.query, tab: v === defaultTab.value ? undefined : v } }) },
})
function tabKey(e: KeyboardEvent) {
  if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
  tab.value = tab.value === 'log' ? 'changes' : 'log'
  document.getElementById('tab-' + tab.value)?.focus()
}
const changesCount = computed(() => {
  if (isAnsible.value) return hostRows.value.length ? String(hostRows.value.length) : ''
  const n = plan.value?.changes.length ?? 0
  return !n ? '' : applyStarted.value ? `${doneCount.value} / ${n}` : String(n)
})
const applyStarted = computed(() => detail.value?.stages.some((s) => s.name === 'apply' && s.status !== 'pending' && s.status !== 'skipped'))
// a tile or host name opens the log searched for it
function openLog(q: string) { level.value = ''; query.value = q; tab.value = 'log' }

const subtitle = computed(() => {
  const r = run.value
  if (!r) return ''
  const bits = r.target.tool === 'ansible'
    ? [[r.target.project === '.' ? '' : r.target.project, r.target.inventory].filter(Boolean).join(' · ') || 'project root', `env ${r.target.env}`, ...(r.target.limit ? [`limit ${r.target.limit}`] : [])]
    : [r.target.root + (r.target.workspace ? ` · workspace ${r.target.workspace}` : ''), `env ${r.target.env}`]
  if (r.commit) bits.push('commit ' + r.commit.slice(0, 7))
  if (r.started_at) bits.push('started ' + new Date(r.started_at).toLocaleTimeString([], { hour12: false }))
  return bits.join(' · ')
})
const elapsed = computed(() => (run.value ? duration(run.value.started_at ?? run.value.created_at, run.value.ended_at, now.value) : ''))

// ---- actions ----
function flash(m: string) { notice.value = m; setTimeout(() => (notice.value = ''), 3000) }
const quote = (a: string) => (/^[\w@%+=:,./-]+$/.test(a) ? a : `'${a.replace(/'/g, `'\\''`)}'`)
async function copyCommand() {
  const argv = (run.value?.argv ?? []).map((a) => quote(String(a)))
  try { await navigator.clipboard.writeText(argv.join(' ')); flash('Command copied') } catch { flash('Copy failed') }
}
async function cancelRun() {
  if (!run.value) return
  const waiting = run.value.status === 'waiting_approval'
  if (!window.confirm(waiting ? 'Discard this plan?' : 'Cancel this run? Terraform will be interrupted and asked to release its state lock.')) return
  try { await runs.cancel(run.value.id) } catch (e) { flash(e instanceof Error ? e.message : String(e)) }
}
const downloadHref = computed(() => (id.value ? `${apiBase}/runs/${id.value}/log/download` : '#'))
</script>

<template>
  <AppShell>
    <div class="split">
      <section class="runs" aria-label="Runs">
        <div class="rhead">
          <h2>Runs</h2>
          <span class="mono muted small">{{ runs.list.length }} shown</span>
        </div>
        <div class="filters" role="group" aria-label="Filter runs">
          <button v-for="f in [['all', 'All'], ['active', 'Active'], ['approval', 'Needs approval'], ['failed', 'Failed']]" :key="f[0]"
            class="btn sm chip" :class="{ on: filter === f[0] }" type="button" :aria-pressed="filter === f[0]" @click="filter = f[0] as typeof filter">{{ f[1] }}</button>
        </div>
        <div class="rlist">
          <RouterLink v-for="r in shown" :key="r.id" :to="'/runs/' + r.id" class="rr" :class="{ on: r.id === id }">
            <span class="dot" :class="dotClass(r.status)" aria-hidden="true" />
            <span class="rtext">
              <span class="mono cmd">{{ kindLabel(r) }}</span>
              <span class="small muted">#{{ r.id }} · {{ r.target.env }}</span>
            </span>
            <span class="mono small muted">{{ ago(r.created_at, now) }}</span>
            <span class="sr-only">{{ r.status }}</span>
          </RouterLink>
          <p v-if="!shown.length" class="muted pad">{{ runs.list.length ? 'No runs match.' : 'No runs yet. Start a plan from the Overview.' }}</p>
        </div>
      </section>

      <div class="detail">
        <p v-if="loadError" class="pad err" role="alert">{{ loadError }}</p>
        <p v-else-if="!run" class="pad muted">{{ id == null ? 'Select a run.' : 'Loading…' }}</p>
        <template v-else>
          <header class="dhead">
            <div class="between">
              <div class="titles">
                <div class="row">
                  <h1 class="mono">#{{ run.id }} {{ kindLabel(run) }}</h1>
                  <StatusPill :status="run.status" />
                </div>
                <span class="muted sub">{{ subtitle }} · <span class="mono">{{ elapsed }}</span></span>
              </div>
              <div class="actions">
                <button class="btn sm" type="button" @click="copyCommand">Copy command</button>
                <a class="btn sm" :href="downloadHref" download>Download log</a>
                <button v-if="active" class="btn sm danger" type="button" @click="cancelRun">{{ run.status === 'waiting_approval' ? 'Discard plan' : 'Cancel run' }}</button>
              </div>
            </div>
            <StagePipeline :stages="detail!.stages" :plan="plan" />
            <p v-if="notice" class="ok small" role="status">{{ notice }}</p>
            <p v-if="detail!.summary.error" class="banner fail" role="alert">{{ detail!.summary.error }}</p>
            <p v-else-if="detail!.summary.apply && run.status === 'succeeded'" class="banner ok">
              Applied: {{ detail!.summary.apply.added }} added, {{ detail!.summary.apply.changed }} changed, {{ detail!.summary.apply.destroyed }} destroyed<template v-if="detail!.summary.apply.outputs?.length"> · outputs: <span class="mono">{{ detail!.summary.apply.outputs.join(', ') }}</span></template>
            </p>
            <p v-else-if="detail!.summary.no_changes" class="banner ok">No changes. Infrastructure matches the configuration.</p>
            <p v-else-if="detail!.summary.drift && run.status === 'succeeded'" class="banner" :class="detail!.summary.drift.resources ? 'warnb' : 'ok'">
              <template v-if="detail!.summary.drift.resources">Drift: {{ detail!.summary.drift.resources }} resource(s) differ from what Terraform recorded.
                <RouterLink :to="{ path: '/graph', query: { root: run.target.root, env: run.target.env } }">Review in Graph →</RouterLink></template>
              <template v-else>No drift: real infrastructure matches the state.</template>
            </p>
          </header>

          <div class="tabs" role="tablist" aria-label="Run detail">
            <button id="tab-changes" class="tab" role="tab" type="button" :aria-selected="tab === 'changes'" aria-controls="panel-changes" :tabindex="tab === 'changes' ? 0 : -1" @click="tab = 'changes'" @keydown="tabKey">
              Changes<span v-if="changesCount" class="n mono">{{ changesCount }}</span></button>
            <button id="tab-log" class="tab" role="tab" type="button" :aria-selected="tab === 'log'" aria-controls="panel-log" :tabindex="tab === 'log' ? 0 : -1" @click="tab = 'log'" @keydown="tabKey">
              Log<span v-if="warnCount" class="n mono w">{{ warnCount }} warn</span><span v-if="errCount" class="n mono e">{{ errCount }} error</span></button>
          </div>

          <div v-if="tab === 'changes'" id="panel-changes" class="vis" role="tabpanel" aria-labelledby="tab-changes">
            <section v-if="isAnsible" class="changes" aria-label="Hosts">
              <div class="between">
                <h2>Hosts</h2>
                <div v-if="hasPhase('check') && hasPhase('run')" class="phase" role="group" aria-label="Phase">
                  <button class="btn sm chip" :class="{ on: phase === 'check' }" type="button" :aria-pressed="phase === 'check'" @click="phase = 'check'">Dry run</button>
                  <button class="btn sm chip" :class="{ on: phase === 'run' }" type="button" :aria-pressed="phase === 'run'" @click="phase = 'run'">Run</button>
                </div>
                <span v-else class="small muted">{{ phase === 'check' ? 'dry run' : '' }}</span>
              </div>
              <div v-if="run.status === 'waiting_approval'" class="agate">
                <span class="gt">Run {{ run.target.playbook }} on {{ run.target.env }}</span>
                <span class="small muted">The dry run below shows what would change. Approving runs the playbook for real.</span>
                <label v-if="detail!.target.approval_required" class="field small">Type <b class="mono">{{ run.target.env }}</b> to confirm
                  <input v-model="typed" class="inp" type="text" autocomplete="off" :aria-label="`Type ${run.target.env} to confirm`" />
                </label>
                <p v-if="approveError" class="err small" role="alert">{{ approveError }}</p>
                <div class="row">
                  <button class="btn primary" type="button" :disabled="approving || (detail!.target.approval_required && typed.trim() !== run.target.env)" @click="approveAnsible">Run for real</button>
                  <button class="btn sm" type="button" @click="rerunCheck">Re-run dry run</button>
                </div>
              </div>
              <RunHostGrid v-if="hostRows.length" :results="phaseResults" :hosts="hostRows" @open="openLog" />
              <p v-else class="muted small">{{ run.status === 'queued' || run.status === 'running' ? 'Waiting for results…' : 'No host results.' }}</p>
            </section>
            <section v-else class="changes" aria-label="Planned changes">
              <div class="between">
                <h2>Changes</h2>
                <span v-if="plan" class="mono small">
                  <span style="color: var(--ok)">+{{ plan.create }}</span> <span style="color: var(--running)">~{{ plan.update }}</span>
                  <span style="color: var(--warn)">±{{ plan.replace }}</span> <span style="color: var(--fail)">−{{ plan.delete }}</span>
                </span>
              </div>
              <RouterLink v-if="run.status === 'waiting_approval'" class="btn primary self" :to="`/runs/${run.id}/plan`">Review plan and approve →</RouterLink>
              <RunResourceMap v-if="plan?.changes.length" :changes="plan.changes" :states="states" @open="openLog" />
              <p v-else-if="!plan" class="muted small">{{ run.status === 'queued' || run.status === 'running' ? 'Planning…' : 'No plan for this run.' }}</p>
              <p v-else class="muted small">Nothing to change.</p>
              <RouterLink v-if="plan?.changes.length" class="small" :to="`/runs/${run.id}/plan`">View full plan diff →</RouterLink>
            </section>
          </div>

          <section v-else id="panel-log" class="logpane" role="tabpanel" aria-labelledby="tab-log">
            <div class="ltools">
              <label class="search">
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><circle cx="11" cy="11" r="7" /><path d="M20 20l-4-4" /></svg>
                <input v-model="query" type="search" placeholder="Search log" aria-label="Search log" />
              </label>
              <div role="group" aria-label="Level" class="lv">
                <button class="btn sm chip" :class="{ on: level === '' }" type="button" :aria-pressed="level === ''" @click="level = ''">All</button>
                <button class="btn sm chip warn" :class="{ on: level === 'warn' }" type="button" :aria-pressed="level === 'warn'" @click="level = level === 'warn' ? '' : 'warn'">Warn {{ warnCount }}</button>
                <button class="btn sm chip fail" :class="{ on: level === 'error' }" type="button" :aria-pressed="level === 'error'" @click="level = level === 'error' ? '' : 'error'">Error {{ errCount }}</button>
              </div>
              <label class="tog"><input v-model="follow" type="checkbox" />Follow</label>
              <label class="tog"><input v-model="wrap" type="checkbox" />Wrap</label>
            </div>
            <p v-if="log.skipped.value > 0" class="note small">
              Showing the last {{ log.lines.value.length.toLocaleString() }} lines.
              <button class="link" type="button" @click="log.reload(true)">Load all {{ log.total.value.toLocaleString() }}</button>
            </p>
            <div class="logbox"><LogView v-model:follow="follow" :lines="log.lines.value" :wrap="wrap" /></div>
          </section>
        </template>
      </div>
    </div>
  </AppShell>
</template>

<style scoped>
/* one row on desktop, so the log has a definite height and scrolls inside it */
.split { display: flex; flex: 1; min-height: 0; }
.runs { flex: 1 1 272px; max-width: 320px; border-right: 1px solid var(--line); padding: 16px 10px; display: flex; flex-direction: column; gap: 10px; background: var(--bg-sunken); overflow: auto; }
.rhead { display: flex; align-items: center; justify-content: space-between; padding: 0 6px; }
h1 { margin: 0; font-size: 20px; font-weight: 700; }
h2 { margin: 0; font-size: 14px; font-weight: 600; }
.small { font-size: 13px; }
.filters { display: flex; flex-wrap: wrap; gap: 6px; padding: 0 6px; }
.chip { height: 28px; font-size: 13px; padding: 0 10px; }
.chip.on { background: var(--bg-active); }
.chip.warn { color: var(--warn); } .chip.fail { color: var(--fail-strong); }
.rlist { display: flex; flex-direction: column; gap: 2px; }
.rr { display: grid; grid-template-columns: 10px minmax(0, 1fr) auto; gap: 10px; align-items: center; padding: 10px 14px; border-radius: 6px; color: inherit; text-decoration: none; }
.rr:hover { background: var(--bg-raised); }
.rr.on { background: var(--bg-active); box-shadow: inset 0 0 0 1px var(--line-strong); }
.rtext { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.cmd { font-size: 13.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--text-disabled); }
.d-ok { background: var(--ok); } .d-run { background: var(--running); } .d-fail { background: var(--fail); } .d-warn { background: var(--warn); }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
.detail { flex: 999 1 600px; min-width: 0; display: flex; flex-direction: column; min-height: 0; }
.dhead { padding: 20px 24px; border-bottom: 1px solid var(--line); display: flex; flex-direction: column; gap: 16px; }
.between { display: flex; flex-wrap: wrap; align-items: flex-start; justify-content: space-between; gap: 12px; }
.titles { display: flex; flex-direction: column; gap: 6px; }
.row { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.sub { font-size: 14px; }
.actions { display: flex; flex-wrap: wrap; gap: 8px; }
.btn.danger { border-color: var(--fail-line); color: var(--fail-strong); }
.banner { margin: 0; padding: 10px 14px; border-radius: 8px; font-size: 14px; }
.banner.fail { background: var(--fail-bg); border: 1px solid var(--fail-line); color: var(--fail-strong); }
.banner.ok { background: var(--ok-bg); border: 1px solid var(--ok-line); color: var(--ok); }
.banner.warnb { background: var(--warn-bg); border: 1px solid var(--warn-line); color: var(--warn-strong); }
.tabs { display: flex; gap: 2px; padding: 0 24px; border-bottom: 1px solid var(--line); }
.tab { all: unset; display: inline-flex; align-items: center; gap: 8px; padding: 10px 12px 9px; margin-bottom: -1px; border-bottom: 2px solid transparent; font-size: 14px; font-weight: 500; color: var(--text-muted); cursor: pointer; }
.tab:hover { color: var(--text); }
.tab[aria-selected='true'] { color: var(--text); border-bottom-color: var(--accent); }
.tab:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; border-radius: 4px; }
.n { font-size: 12px; color: var(--text-faint); }
.n.w { color: var(--warn); } .n.e { color: var(--fail-strong); }
.vis { flex: 1; min-height: 0; overflow: auto; background-color: var(--bg-panel); background-image: radial-gradient(var(--line) 1px, transparent 1px); background-size: 18px 18px; }
.changes { padding: 18px 24px 24px; display: flex; flex-direction: column; gap: 14px; }
.self { align-self: flex-start; }
/* min-height 0, like .vis: a floor here would push the page past the viewport on short windows */
.logpane { flex: 1; min-width: 0; min-height: 0; display: flex; flex-direction: column; background: var(--bg-inset); }
.ltools { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 10px 16px; border-bottom: 1px solid var(--line); }
.search { flex: 1 1 200px; display: flex; align-items: center; gap: 8px; height: 30px; padding: 0 10px; border: 1px solid var(--line-strong); border-radius: 6px; color: var(--text-label); }
.search input { flex: 1; min-width: 0; background: transparent; border: 0; outline: none; color: var(--text); font-size: 13px; }
.lv { display: flex; gap: 4px; }
.tog { display: flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text-muted); }
.logbox { flex: 1; min-height: 0; }
.note { margin: 0; padding: 6px 16px; color: var(--text-muted); border-bottom: 1px solid var(--line); }
.link { background: none; border: 0; color: var(--accent); cursor: pointer; padding: 0; font: inherit; }
.pad { padding: 24px; margin: 0; }
.phase { display: flex; gap: 4px; }
.agate { display: flex; flex-direction: column; gap: 10px; padding: 14px; border: 1px solid var(--warn-line); border-radius: 8px; background: var(--warn-bg); }
.gt { font-weight: 600; }
@media (max-width: 900px) { .split { flex-wrap: wrap; } .runs { max-width: 100%; border-right: 0; } .logpane { min-height: 320px; } }
</style>
