<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from '../components/AppShell.vue'
import { api, apiFetch } from '../api/client'
import type { DoctorReport, Issue } from '../api/types'
import { useIssues } from '../stores/issues'
import { ago } from '../stores/runs'
import { onEvent } from '../composables/events'
import { useNow } from '../composables/now'

const route = useRoute()
const router = useRouter()
const issues = useIssues()
const now = useNow(15_000)

const selectedId = computed(() => (route.params.id ? Number(route.params.id) : issues.open[0]?.id ?? 0))
const detail = ref<Issue | null>(null)
const loadErr = ref('')
async function loadDetail() {
  const id = selectedId.value
  if (!id) { detail.value = null; return }
  try {
    const d = await api<Issue>(`/issues/${id}`)
    if (id === selectedId.value) detail.value = d
    loadErr.value = ''
  } catch (e) { loadErr.value = e instanceof Error ? e.message : String(e) }
}
watch(selectedId, () => { Object.assign(act, { step: -1, confirm: '', error: '', notice: '' }); loadDetail() })
onEvent('issues.updated', () => window.setTimeout(loadDetail, 250))
onMounted(async () => {
  await issues.refresh()
  loadDetail()
  loadDoctor()
  if (route.query.doctor === '1') runDoctor()
})

const PILL: Record<string, string> = { error: 'st-fail', warning: 'st-warn', info: 'st-idle' }
const since = (t: string) => { const a = ago(t, now.value); return a === 'now' ? 'just now' : a + ' ago' }

// ---- actions ----
const act = reactive({ step: -1, confirm: '', error: '', notice: '', busy: false })
async function runStep(i: number) {
  const d = detail.value
  if (!d) return
  act.busy = true; act.error = ''; act.step = i
  try {
    const r = await api<{ run: { id: number } }>(`/issues/${d.id}/act`, { body: { step: i, confirm: act.confirm } })
    act.notice = `Started run #${r.run.id}.`
    act.confirm = ''
    router.push('/runs/' + r.run.id)
  } catch (e) { act.error = e instanceof Error ? e.message : String(e) } finally { act.busy = false }
}
const copied = ref('')
async function copy(text: string) {
  try { await navigator.clipboard.writeText(text); copied.value = text; setTimeout(() => (copied.value = ''), 2000) } catch { /* clipboard blocked */ }
}
async function markResolved() {
  const d = detail.value
  if (!d) return
  await api(`/issues/${d.id}/resolve`, { method: 'POST' })
  await issues.refresh()
  router.replace('/troubleshoot')
}
const href = (h: string) => h // rule hrefs are app routes

// ---- report ----
async function exportReport() {
  const res = await apiFetch('/issues/export')
  const blob = await res.blob()
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = 'groundwork-report.md'
  a.click()
  URL.revokeObjectURL(a.href)
}

// ---- doctor ----
const doctor = ref<DoctorReport | null>(null)
const doctorRunning = ref(false)
async function loadDoctor() { doctor.value = (await api<{ report: DoctorReport | null }>('/doctor')).report }
async function runDoctor() {
  doctorRunning.value = true
  try { doctor.value = (await api<{ report: DoctorReport }>('/doctor/run', { method: 'POST' })).report } finally { doctorRunning.value = false }
}
const DOC: Record<string, string> = { ok: 'st-ok', warn: 'st-warn', fail: 'st-fail', missing: 'st-warn' }
const factLabel = (k: string) => k.replace(/_/g, ' ').replace(/^lock id$/, 'lock ID')
</script>

<template>
  <AppShell>
    <header class="thead">
      <h1>Troubleshoot</h1>
      <span class="muted small">{{ issues.open.length }} open · {{ issues.resolvedThisWeek }} resolved this week</span>
      <div class="actions">
        <button class="btn sm" type="button" @click="exportReport">Export report</button>
        <button class="btn sm primary" type="button" :disabled="doctorRunning" @click="runDoctor">{{ doctorRunning ? 'Running doctor…' : 'Run doctor' }}</button>
      </div>
    </header>
    <p v-for="e in issues.ruleErrors" :key="e" class="err pad2" role="alert">Rule file skipped: {{ e }}</p>

    <div class="cols">
      <section class="list" aria-label="Open issues">
        <span class="lbl pl">Open</span>
        <RouterLink v-for="i in issues.open" :key="i.id" class="iss" :class="{ on: i.id === selectedId }" :to="'/troubleshoot/' + i.id">
          <span class="row"><span class="pill" :class="PILL[i.severity] ?? 'st-idle'">{{ i.category }}</span><span class="small muted">{{ since(i.last_seen) }}</span></span>
          <span class="mono ititle">{{ i.title }}</span>
          <span class="mono small muted">{{ i.target }}</span>
        </RouterLink>
        <p v-if="issues.loaded && !issues.open.length" class="small muted pl">Nothing needs attention.</p>
        <span class="lbl pl top">Recently resolved</span>
        <RouterLink v-for="i in issues.resolved" :key="i.id" class="iss done" :to="'/troubleshoot/' + i.id">
          <span class="mono ititle">{{ i.title }}</span>
          <span class="small muted">{{ i.resolution }} · {{ i.resolved_at ? since(i.resolved_at) : '' }}</span>
        </RouterLink>
        <p v-if="issues.loaded && !issues.resolved.length" class="small muted pl">None this week.</p>
      </section>

      <section v-if="detail" class="detail" aria-label="Issue detail">
        <div class="col">
          <div class="row wrap"><span class="pill" :class="PILL[detail.severity] ?? 'st-idle'">{{ detail.category }}</span><span class="mono small muted">{{ detail.target }}</span>
            <span v-if="detail.status === 'resolved'" class="pill st-ok">resolved</span></div>
          <h2 class="dtitle">{{ detail.title }}</h2>
          <p class="explain">{{ detail.explain }}</p>
          <p v-if="detail.status === 'resolved'" class="small ok">{{ detail.resolution }}</p>
        </div>

        <div class="cards">
          <div v-if="detail.facts && Object.keys(detail.facts).length" class="card2">
            <span class="lbl">Details</span>
            <div class="kv mono">
              <template v-for="(v, k) in detail.facts" :key="k"><span>{{ factLabel(String(k)) }}</span><span class="v">{{ v }}</span></template>
            </div>
          </div>
          <div class="card2">
            <span class="lbl">What happened</span>
            <ol class="mono tl">
              <li><span class="muted">{{ new Date(detail.first_seen).toLocaleTimeString() }}</span><span>first seen<template v-if="detail.run_id"> in run #{{ detail.run_id }}</template></span></li>
              <li v-if="detail.last_seen !== detail.first_seen"><span class="muted">{{ new Date(detail.last_seen).toLocaleTimeString() }}</span><span>seen again</span></li>
              <li v-for="(c, i) in detail.checks ?? []" :key="i"><span class="muted">now</span><span :class="c.ok ? '' : 'fail'">{{ c.detail }}</span></li>
            </ol>
            <RouterLink v-if="detail.run_id" class="small" :to="'/runs/' + detail.run_id">Open run #{{ detail.run_id }} log →</RouterLink>
          </div>
        </div>

        <div v-if="detail.status === 'open'" class="fix">
          <span class="lbl">Fix it</span>
          <div v-if="detail.checks?.length" class="step">
            <span class="num" :class="{ done: detail.checks.every((c) => c.ok), bad: !detail.checks.every((c) => c.ok) }">{{ detail.checks.every((c) => c.ok) ? '✓' : '!' }}</span>
            <div class="col"><span class="stitle">Make sure nothing is still running</span>
              <span class="small muted">{{ detail.checks.every((c) => c.ok) ? 'Checked: ' : '' }}{{ detail.checks.map((c) => c.detail).join(' ') }}</span></div>
          </div>
          <div v-for="(s, i) in detail.steps" :key="i" class="step">
            <span class="num">{{ i + 1 + (detail.checks?.length ? 1 : 0) }}</span>
            <div class="col grow">
              <span class="stitle">{{ s.title }}</span>
              <span v-if="s.detail" class="small muted">{{ s.detail }}</span>
              <template v-if="s.action">
                <code v-if="s.action.command || (s.action.kind === 'copy' && s.action.text)" class="mono cmd">{{ s.action.command || s.action.text }}</code>
                <div class="row wrap end">
                  <label v-if="s.action.kind === 'run' && s.action.confirm" class="field"><span>Type <span class="mono strong">{{ s.action.confirm }}</span> to confirm</span>
                    <input v-model="act.confirm" class="inp" type="text" autocomplete="off" :aria-label="`Type ${s.action.confirm} to confirm`" />
                  </label>
                  <button v-if="s.action.kind === 'run'" class="btn" :class="s.action.mutating ? 'danger' : ''" type="button"
                    :disabled="act.busy || (!!s.action.confirm && act.confirm !== s.action.confirm) || (s.action.mutating && !!detail.checks?.some((c) => !c.ok))"
                    @click="runStep(i)">{{ s.action.label }}</button>
                  <RouterLink v-else-if="s.action.kind === 'open' && s.action.href" class="btn" :to="href(s.action.href)">{{ s.action.label }}</RouterLink>
                  <button v-if="s.action.kind === 'copy' || s.action.command" class="btn" type="button" @click="copy(s.action.command || s.action.text || '')">{{ copied && copied === (s.action.command || s.action.text) ? 'Copied' : s.action.kind === 'copy' ? s.action.label : 'Copy command' }}</button>
                </div>
                <p v-if="act.step === i && act.error" class="err" role="alert">{{ act.error }}</p>
              </template>
            </div>
          </div>
          <button class="btn ghost" type="button" @click="markResolved">Mark resolved</button>
        </div>

        <details v-if="detail.excerpt" class="raw">
          <summary>Raw output<template v-if="detail.run_id"> from run #{{ detail.run_id }}</template></summary>
          <pre class="mono">{{ detail.excerpt }}</pre>
        </details>
      </section>
      <section v-else class="detail empty">
        <p class="muted">{{ loadErr || (issues.loaded ? 'No open issues. Failed runs and check findings with a known cause show up here with a fix.' : 'Loading…') }}</p>
      </section>
    </div>

    <section class="doctor" aria-label="Doctor">
      <div class="dh">
        <h2>Doctor · environment checks</h2>
        <span class="mono small muted">{{ doctor ? 'ran ' + since(doctor.ran_at) : 'not run yet' }}</span>
      </div>
      <div v-if="doctor" class="dgrid">
        <div v-for="c in doctor.checks" :key="c.id" class="dc">
          <span class="dname">{{ c.name }}</span>
          <span class="pill" :class="DOC[c.status]">{{ c.status }}</span>
          <span class="mono small muted">{{ c.detail }}<template v-if="c.hint && c.status !== 'ok'"> · <span class="hint">{{ c.hint }}</span></template></span>
        </div>
      </div>
      <p v-else class="pad muted small">Doctor checks tools, credentials, state backends, the SSH agent, inventory reachability and disk space. <button class="link" type="button" @click="runDoctor">Run it now</button></p>
    </section>
  </AppShell>
</template>

<style scoped>
.thead { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 14px 24px; border-bottom: 1px solid var(--line); }
h1 { margin: 0; font-size: 18px; font-weight: 600; }
h2 { margin: 0; font-size: 14px; font-weight: 600; }
.small { font-size: 13px; } .ok { color: var(--ok); } .fail { color: var(--fail); }
.pad { padding: 16px 18px; margin: 0; } .pad2 { padding: 8px 24px; margin: 0; }
.actions { margin-left: auto; display: flex; gap: 8px; }
.cols { display: flex; flex-wrap: wrap; align-items: stretch; }
.list { flex: 1 1 300px; max-width: 100%; box-sizing: border-box; padding: 16px 12px; border-right: 1px solid var(--line); display: flex; flex-direction: column; gap: 6px; background: #0F1315; }
@media (min-width: 900px) { .list { max-width: 340px; } }
.pl { padding: 4px 8px; } .top { padding-top: 16px; }
.iss { display: flex; flex-direction: column; gap: 4px; padding: 10px 12px; border-radius: 8px; border: 1px solid transparent; color: var(--text); text-decoration: none; }
.iss:hover { background: #151A1E; } .iss.on { background: #161D12; border-color: #2E3A22; }
.iss.done { opacity: .75; }
.iss:focus-visible { outline: 2px solid var(--accent); }
.ititle { font-size: 14px; }
.row { display: flex; align-items: center; gap: 8px; } .wrap { flex-wrap: wrap; } .end { align-items: flex-end; }
.col { display: flex; flex-direction: column; gap: 6px; } .grow { flex: 1; min-width: 0; }
.detail { flex: 999 1 520px; min-width: 0; padding: 24px; display: flex; flex-direction: column; gap: 22px; }
.detail.empty { justify-content: center; }
.dtitle { font-size: 22px; font-weight: 600; letter-spacing: -.01em; }
.explain { margin: 0; color: #C8CFD4; line-height: 1.55; max-width: 760px; }
.pill { font: 500 12px var(--font-mono); padding: 3px 8px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; white-space: nowrap; align-self: flex-start; }
.st-ok { color: var(--ok); background: rgba(95, 211, 141, .10); } .st-fail { color: var(--fail); background: rgba(255, 122, 122, .10); }
.st-warn { color: var(--warn); background: rgba(245, 182, 71, .10); } .st-idle { color: var(--text-muted); background: rgba(154, 164, 171, .10); }
.cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(300px, 100%), 1fr)); gap: 16px; }
.card2 { border: 1px solid var(--line); border-radius: 10px; background: var(--bg-panel); padding: 16px; display: flex; flex-direction: column; gap: 10px; }
.kv { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 6px 14px; font-size: 13.5px; color: var(--text-muted); }
.kv .v { color: var(--text); overflow-wrap: anywhere; }
.tl { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; font-size: 13.5px; }
.tl li { display: grid; grid-template-columns: 80px minmax(0, 1fr); gap: 10px; }
.fix { border: 1px solid var(--line); border-radius: 10px; background: var(--bg-panel); padding: 18px; display: flex; flex-direction: column; gap: 16px; }
.step { display: flex; gap: 12px; }
.num { flex: none; width: 24px; height: 24px; border-radius: 50%; display: grid; place-items: center; font: 600 13px var(--font-mono); border: 1px solid var(--line-strong); color: var(--text-muted); }
.num.done { background: rgba(95, 211, 141, .15); border-color: transparent; color: var(--ok); }
.num.bad { background: rgba(255, 122, 122, .15); border-color: transparent; color: var(--fail); }
.stitle { font-weight: 500; }
.cmd { font-size: 13.5px; background: var(--bg-inset, #0A0C0E); border: 1px solid var(--line); border-radius: 6px; padding: 10px 12px; overflow-x: auto; white-space: nowrap; }
.field { display: flex; flex-direction: column; gap: 6px; font-size: 13px; color: var(--text-muted); }
.field .inp { width: 240px; max-width: 100%; height: 34px; }
.strong { color: var(--text); }
.btn.danger { background: rgba(255, 122, 122, .12); border-color: #5A2C2C; color: var(--fail); }
.btn.ghost { align-self: flex-start; background: transparent; border-color: transparent; color: var(--text-muted); }
.raw { border: 1px solid var(--line); border-radius: 10px; background: var(--bg-inset, #0A0C0E); }
.raw summary { padding: 12px 16px; cursor: pointer; font-size: 14px; color: var(--text-muted); }
.raw pre { margin: 0; padding: 0 16px 16px; font-size: 13px; line-height: 1.6; color: #FF9A9A; white-space: pre-wrap; overflow-wrap: anywhere; }
.doctor { margin: 0 24px 28px; border: 1px solid var(--line); border-radius: 10px; background: var(--bg-panel); }
.dh { display: flex; align-items: center; justify-content: space-between; padding: 14px 18px; border-bottom: 1px solid var(--line); }
.dgrid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(320px, 100%), 1fr)); }
.dc { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 4px 12px; align-items: center; padding: 12px 18px; border-bottom: 1px solid #1A1F23; }
.dc .pill { grid-row: span 2; align-self: center; }
.dname { font-weight: 500; font-size: 14px; }
.hint { color: var(--accent); }
.link { background: none; border: 0; color: var(--accent); cursor: pointer; padding: 0; font: inherit; }
</style>
