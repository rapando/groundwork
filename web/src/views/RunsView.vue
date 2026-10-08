<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from '../components/AppShell.vue'
import StatusPill from '../components/StatusPill.vue'
import StagePipeline from '../components/StagePipeline.vue'
import LogView from '../components/LogView.vue'
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
watch(id, () => { resMap.clear(); results.value = []; openHost.value = ''; loadResources(); loadResults(); loadDetail() }, { immediate: false })
onEvent('run.updated', (d: { id: number }) => { if (d.id === id.value) loadDetail() })
onEvent('reconnected', () => loadDetail())

onMounted(async () => {
  await runs.refresh()
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
const openHost = ref('')
async function loadResults() {
  if (id.value == null) return
  try {
    const r = await api<{ results: HostResult[] }>(`/runs/${id.value}/hosts`)
    results.value = r.results
    if (r.results.some((x) => x.phase === 'run')) phase.value = 'run'
  } catch { results.value = [] }
}
onEvent('ans.result', (d: { run: number; result: HostResult }) => {
  if (d.run !== id.value) return
  results.value = results.value.concat(d.result)
  if (d.result.phase === 'run') phase.value = 'run'
})
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
const worstCls: Record<string, string> = { ok: 'ok', changed: 'warn', failed: 'fail', unreachable: 'fail', skipped: 'idle' }

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
const sym: Record<string, { s: string; c: string }> = {
  create: { s: '+', c: 'var(--ok)' }, update: { s: '~', c: 'var(--running)' },
  replace: { s: '±', c: 'var(--warn)' }, delete: { s: '−', c: 'var(--fail)' }, read: { s: '·', c: 'var(--text-label)' },
}
function note(c: PlanChange) {
  if (c.action === 'replace') return 'forces replacement' + (c.replace_paths?.length ? ': ' + c.replace_paths.map((p) => p.join('.')).join(', ') : '')
  return { create: 'will be created', update: 'will be updated in place', delete: 'will be destroyed', read: 'will be read' }[c.action] ?? c.action
}
function stateOf(c: PlanChange): { text: string; cls: string } {
  const r = resMap.get(c.address)
  if (r?.state === 'done') return { text: 'done', cls: 'ok' }
  if (r?.state === 'failed') return { text: 'failed', cls: 'fail' }
  if (r?.state === 'running') return { text: r.elapsed ? `running ${r.elapsed}s` : 'running', cls: 'run' }
  if (run.value?.status === 'succeeded' && detail.value?.summary.apply) return { text: 'done', cls: 'ok' }
  return { text: 'planned', cls: 'idle' }
}
const doneCount = computed(() => (plan.value?.changes ?? []).filter((c) => stateOf(c).cls === 'ok').length)
const progress = computed(() => (plan.value?.changes.length ? (doneCount.value / plan.value.changes.length) * 100 : 0))
const applying = computed(() => detail.value?.stages.some((s) => s.name === 'apply' && s.status !== 'pending' && s.status !== 'skipped'))

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
// allow line breaks after dots and brackets instead of mid-word
const breakable = (a: string) => a.replace(/([.[])/g, '$1\u200b')
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

          <div class="cols">
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
              <div class="clist">
                <div v-for="h in hostRows" :key="h.host" class="change">
                  <button type="button" class="hbtn" :aria-expanded="openHost === h.host" @click="openHost = openHost === h.host ? '' : h.host">
                    <span class="mono addr">{{ h.host }}</span>
                    <span class="pill" :class="'st-' + worstCls[h.worst]">{{ h.worst }}</span>
                  </button>
                  <span class="small muted mono">ok {{ h.ok }} · changed {{ h.changed }} · failed {{ h.failed }}<template v-if="h.unreachable"> · unreachable</template><template v-if="h.skipped"> · skipped {{ h.skipped }}</template></span>
                  <div v-if="openHost === h.host" class="tasks">
                    <div v-for="(t, i) in h.tasks" :key="i" class="task">
                      <div class="between"><span class="small">{{ t.task }}</span><span class="pill" :class="'st-' + worstCls[t.status]">{{ t.status }}{{ t.ignored ? ' (ignored)' : '' }}</span></div>
                      <pre v-if="t.msg" class="tmsg">{{ t.msg }}</pre>
                      <pre v-if="t.diff" class="tdiff">{{ t.diff }}</pre>
                    </div>
                  </div>
                </div>
                <p v-if="!hostRows.length" class="muted small">{{ run.status === 'queued' || run.status === 'running' ? 'Waiting for results…' : 'No host results.' }}</p>
              </div>
            </section>
            <section v-else class="changes" aria-label="Planned changes">
              <div class="between">
                <h2>Changes</h2>
                <span v-if="plan" class="mono small">
                  <span style="color: var(--ok)">+{{ plan.create }}</span> <span style="color: var(--running)">~{{ plan.update }}</span>
                  <span style="color: var(--warn)">±{{ plan.replace }}</span> <span style="color: var(--fail)">−{{ plan.delete }}</span>
                </span>
              </div>
              <RouterLink v-if="run.status === 'waiting_approval'" class="btn primary" :to="`/runs/${run.id}/plan`">Review plan and approve →</RouterLink>
              <div v-if="applying && plan?.changes.length" class="bar" role="img" :aria-label="`${doneCount} of ${plan.changes.length} resources applied`">
                <span :style="{ width: progress + '%' }" />
              </div>
              <div class="clist">
                <div v-for="c in plan?.changes ?? []" :key="c.address" class="change">
                  <div class="row"><span class="mono s" :style="{ color: sym[c.action]?.c }">{{ sym[c.action]?.s }}</span><span class="mono addr">{{ breakable(c.address) }}</span></div>
                  <div class="between"><span class="small muted">{{ note(c) }}</span><span class="pill" :class="'st-' + stateOf(c).cls">{{ stateOf(c).text }}</span></div>
                </div>
                <p v-if="!plan" class="muted small">{{ run.status === 'queued' || run.status === 'running' ? 'Planning…' : 'No plan for this run.' }}</p>
                <p v-else-if="!plan.changes.length" class="muted small">Nothing to change.</p>
              </div>
              <RouterLink v-if="plan?.changes.length" class="small" :to="`/runs/${run.id}/plan`">View full plan diff →</RouterLink>
            </section>

            <section class="logpane" aria-label="Log output">
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
          </div>
        </template>
      </div>
    </div>
  </AppShell>
</template>

<style scoped>
.split { display: flex; flex-wrap: wrap; flex: 1; min-height: 0; }
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
.cols { display: flex; flex-wrap: wrap; flex: 1; min-height: 0; }
.changes { flex: 1 1 300px; max-width: 420px; border-right: 1px solid var(--line); padding: 18px; display: flex; flex-direction: column; gap: 14px; overflow: auto; }
.bar { height: 6px; border-radius: 3px; background: var(--line); overflow: hidden; }
.bar span { display: block; height: 100%; background: var(--ok); transition: width .3s; }
.clist { display: flex; flex-direction: column; gap: 8px; }
.change { display: flex; flex-direction: column; gap: 6px; padding: 10px 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--bg-panel); }
.s { font-size: 14px; font-weight: 700; }
.addr { font-size: 13px; overflow-wrap: break-word; }
.pill { font: 500 12px var(--font-mono); padding: 3px 8px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; }
.st-ok { color: var(--ok); background: color-mix(in srgb, var(--ok) 10%, transparent); } .st-fail { color: var(--fail); background: color-mix(in srgb, var(--fail) 10%, transparent); }
.st-run { color: var(--running); background: color-mix(in srgb, var(--running) 12%, transparent); } .st-idle { color: var(--text-muted); background: color-mix(in srgb, var(--text-muted) 10%, transparent); }
.logpane { flex: 999 1 420px; min-width: 0; min-height: 320px; display: flex; flex-direction: column; background: var(--bg-inset); }
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
.hbtn { all: unset; display: flex; align-items: center; justify-content: space-between; gap: 8px; cursor: pointer; }
.hbtn:focus-visible { outline: 2px solid var(--accent); }
.st-warn { color: var(--warn); background: color-mix(in srgb, var(--warn) 10%, transparent); }
.tasks { display: flex; flex-direction: column; gap: 8px; border-top: 1px solid var(--line); padding-top: 8px; }
.task { display: flex; flex-direction: column; gap: 4px; }
.tmsg, .tdiff { margin: 0; font: 12.5px/1.5 var(--font-mono); white-space: pre-wrap; overflow-wrap: anywhere; background: var(--bg-inset); padding: 6px 8px; border-radius: 4px; max-height: 240px; overflow: auto; }
.tmsg { color: var(--fail-strong); }
.tdiff { color: var(--text-code); }
@media (max-width: 900px) { .runs, .changes { max-width: 100%; border-right: 0; } }
</style>
