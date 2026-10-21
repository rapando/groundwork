<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from '../components/AppShell.vue'
import GraphCanvas, { type GEdge, type GNode } from '../components/GraphCanvas.vue'
import DropMenu from '../components/DropMenu.vue'
import SystemPanel from '../components/SystemPanel.vue'
import DiffView from '../components/DiffView.vue'
import { api, ApiError } from '../api/client'
import { swallow } from '../api/swallow'
import type { DriftAttr, DriftResource, DriftSchedule, GraphModel } from '../api/types'
import { since, STATUS_LABEL, useRuns } from '../stores/runs'
import { useWorkspace } from '../stores/workspace'
import { onEvent } from '../composables/events'
import { useNow } from '../composables/now'
import { asFilter, fadeMap, FILTERS, filterCounts, findNodes, reach, riskyDownstream, type Filter } from '../composables/graphReach'
import Modal from '../components/Modal.vue'

const route = useRoute()
const router = useRouter()
const ws = useWorkspace()
const runs = useRuns()
const now = useNow(30_000)

// stacks: every (root, env) from groundwork.yaml
const stacks = computed(() => {
  const out: { root: string; env: string }[] = []
  for (const r of ws.ws?.config?.terraform?.roots ?? []) {
    if (r.env) out.push({ root: r.path, env: r.env })
    for (const e of Object.keys(r.envs ?? {})) out.push({ root: r.path, env: e })
  }
  return out
})
const stackKey = computed({
  get: () => `${route.query.root ?? stacks.value[0]?.root ?? ''}|${route.query.env ?? stacks.value[0]?.env ?? ''}`,
  set: (v: string) => { const [root, env] = v.split('|'); router.replace({ query: { ...route.query, root, env } }) },
})
const target = computed(() => { const [root, env] = stackKey.value.split('|'); return { root, env } })
type Mode = 'resources' | 'modules' | 'system'
// ?view=system opens the system view directly
const mode = computed<Mode>({
  get: () => (['modules', 'system'].includes(String(route.query.view)) ? (route.query.view as Mode) : 'resources'),
  set: (v) => { selected.value = ''; only.value = null; router.replace({ query: { ...route.query, view: v === 'resources' ? undefined : v } }) },
})
// ?filter=plan|drifted|errors, so a link (e.g. "Show drifted") opens the graph filtered
const filter = computed<Filter>({
  get: () => asFilter(route.query.filter),
  set: (f) => { router.replace({ query: { ...route.query, filter: f === 'all' ? undefined : f } }) },
})

const model = ref<GraphModel | null>(null)
const planRun = ref<number | null>(null)
const drift = ref<DriftResource[]>([])
const schedule = ref<DriftSchedule | null>(null)
const error = ref('')
async function load() {
  if (!target.value.root) return
  const q = `root=${encodeURIComponent(target.value.root)}&env=${encodeURIComponent(target.value.env)}`
  try {
    const [g, d] = await Promise.all([
      api<{ graph: GraphModel; plan_run: number }>('/graph/deps?' + q),
      api<{ resources: DriftResource[] }>('/drift?' + q),
    ])
    model.value = g.graph
    planRun.value = g.plan_run || null
    drift.value = d.resources
    error.value = ''
  } catch (e) { error.value = e instanceof Error ? e.message : String(e) }
}
onMounted(() => {
  load()
  api<DriftSchedule>('/drift/schedule').then((s) => (schedule.value = s)).catch(swallow('drift schedule'))
  window.addEventListener('keydown', onKey)
})
watch(stackKey, () => { selected.value = ''; only.value = null; load() })
const offs = [
  onEvent('drift.updated', load),
  onEvent('run.updated', (d: { kind: string; status: string }) => { if (d.kind === 'tf.plan' && d.status === 'waiting_approval') load() }),
]
onBeforeUnmount(() => { offs.forEach((off) => off()); window.removeEventListener('keydown', onKey) })

// ---- selection: what the selected resource uses and what uses it, directly or not ----
const selected = ref('')
const node = computed(() => (mode.value === 'resources' ? model.value?.nodes.find((n) => n.id === selected.value) : undefined))
const reached = computed(() => (node.value ? reach(model.value!.edges, node.value.id) : null))
const byDirect = (all: Set<string>, direct: Set<string>) => [...all].sort((a, b) => Number(direct.has(b)) - Number(direct.has(a)) || a.localeCompare(b))
const uses = computed(() => (reached.value ? byDirect(reached.value.up, reached.value.directUp) : []))
const usedBy = computed(() => (reached.value ? byDirect(reached.value.down, reached.value.directDown) : []))
const risky = computed(() => (reached.value ? riskyDownstream(model.value!.nodes, reached.value.down) : []))
const riskText = computed(() => {
  const l = risky.value
  if (!l.length) return ''
  const verb = l.every((n) => n.action === 'delete') ? 'destroyed' : l.every((n) => n.action === 'replace') ? 'replaced' : 'replaced or destroyed'
  const names = l.length > 3 ? `${l.slice(0, 3).map((n) => n.id).join(', ')} and ${l.length - 3} more` : l.map((n) => n.id).join(l.length === 2 ? ' and ' : ', ')
  return `${names}, downstream of this, ${l.length === 1 ? 'is' : 'are'} ${verb} in plan #${planRun.value}.`
})
const nodeDrift = computed(() => drift.value.filter((d) => d.address === selected.value || d.address.replace(/\[[^\]]*\]/g, '') === selected.value))
const codeHref = (f: string, l: number) => `/code/${f.split('/').map(encodeURIComponent).join('/')}?line=${l}`
// "Show only these": draw just the selection and what it reaches (the layout is redone)
const only = ref<Set<string> | null>(null)
function showOnly() { if (reached.value) only.value = new Set([selected.value, ...reached.value.up, ...reached.value.down]) }
// clicking a box selects it (again: clears it); picking from the search or a list also brings it into view
function select(id: string) { selected.value = mode.value === 'resources' && id !== selected.value ? id : '' }
function show(id: string) {
  selected.value = id
  nextTick(() => canvas.value?.center(id))
}

// ---- search and filters: matches stay where they are, the rest fades ----
const query = ref('')
const findInput = ref<HTMLInputElement>()
const counts = computed(() => filterCounts(model.value?.nodes ?? []))
const pool = computed(() => (model.value?.nodes ?? []).filter((n) => !only.value || only.value.has(n.id)))
const matches = computed(() => findNodes(pool.value, filter.value, query.value))
const narrowed = computed(() => filter.value !== 'all' || !!query.value.trim())
let hit = -1
watch([query, filter], () => (hit = -1))
function findNext() {
  if (!query.value.trim() || !matches.value.length) return
  hit = (hit + 1) % matches.value.length
  show(matches.value[hit].id)
}
const isMac = /Mac|iPhone|iPad/.test(navigator.platform)
function onKey(e: KeyboardEvent) {
  if (mode.value !== 'resources' || document.querySelector('[role=dialog]')) return
  if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === 'f') {
    e.preventDefault()
    findInput.value?.focus()
    findInput.value?.select()
  } else if (e.key === 'Escape' && selected.value && !(e.target as Element)?.closest?.('input,select,textarea,[role=menu]')) selected.value = ''
}

// ---- canvas data ----
const short = (id: string) => id.replace(/^module\.[^.]+\./, '')
const nodes = computed<GNode[]>(() => {
  const m = model.value
  if (!m) return []
  if (mode.value === 'modules') {
    const mods = new Map<string, { count: number; changed: boolean; drift: boolean }>()
    for (const n of m.nodes) {
      const k = n.module || '(root)'
      const v = mods.get(k) ?? { count: 0, changed: false, drift: false }
      v.count++
      v.changed ||= !!n.action
      v.drift ||= !!n.drift
      mods.set(k, v)
    }
    return [...mods].map(([k, v]) => ({ id: k, title: 'module', label: k.replace(/^module\./, ''), count: `${v.count} res`, state: v.drift ? 'drift' : v.changed ? 'update' : '' }))
  }
  const r = reached.value
  const fades = r ? null : fadeMap(pool.value.map((n) => n.id), new Set(matches.value.map((n) => n.id)), m.edges)
  return pool.value.map((n) => ({
    id: n.id, title: (n.kind === 'data' ? 'data.' : '') + n.type, label: n.name, count: n.count,
    state: n.drift ? 'drift' : n.action ?? '', errors: n.errors,
    fade: r ? (n.id === selected.value || r.up.has(n.id) || r.down.has(n.id) ? 1 : 0.25) : fades?.get(n.id),
    tag: r?.directUp.has(n.id) ? 'uses' : r?.directDown.has(n.id) ? 'used by' : undefined,
    note: r?.directUp.has(n.id) ? 'the selection uses it' : r?.directDown.has(n.id) ? 'it uses the selection' : undefined,
  }))
})
const edges = computed<GEdge[]>(() => {
  const m = model.value
  if (!m) return []
  if (mode.value === 'resources') return m.edges
  const mod = new Map(m.nodes.map((n) => [n.id, n.module || '(root)']))
  const seen = new Set<string>()
  const out: GEdge[] = []
  for (const e of m.edges) {
    const a = mod.get(e.from)!, b = mod.get(e.to)!
    if (a !== b && !seen.has(a + '>' + b)) { seen.add(a + '>' + b); out.push({ from: a, to: b }) }
  }
  return out
})
const reachIds = computed(() => (reached.value ? [...reached.value.up, ...reached.value.down] : []))
const reachEdges = computed(() => (reached.value ? [...reached.value.edges] : []))
const canvas = ref<InstanceType<typeof GraphCanvas>>()
const exports = computed(() => [
  { label: 'Export SVG', run: () => canvas.value?.exportSVG(`graph-${target.value.env}.svg`) },
  { label: 'Export PNG', run: () => canvas.value?.exportPNG(`graph-${target.value.env}.png`) },
])

// ---- plan overlay ----
const planBrief = computed(() => runs.list.find((r) => r.id === planRun.value))

// ---- drift: progress while a check runs, when it last ran and when it runs next ----
const driftRuns = computed(() => runs.list.filter((r) => r.kind === 'tf.drift' && r.target.root === target.value.root))
const checking = computed(() => {
  const live = driftRuns.value.filter((r) => r.status === 'queued' || r.status === 'running')
  return live.find((r) => r.target.env === target.value.env) ?? live[0]
})
const lastCheck = computed(() => driftRuns.value.find((r) => r.target.env === target.value.env && r.status !== 'queued' && r.status !== 'running'))
const fmtTime = (iso?: string) => (iso ? new Date(iso).toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' }) : '')
const starting = ref(false)
async function detectDrift() {
  starting.value = true
  try {
    await runs.submit({ kind: 'tf.drift', root: target.value.root, env: target.value.env })
  } catch (e) { error.value = e instanceof Error ? e.message : String(e) } finally { starting.value = false }
}
const preview = ref<{ attr: DriftAttr; action: string; diff: string; sha: string; file: string; error: string } | null>(null)
async function act(attr: DriftAttr, action: 'copy' | 'ignore' | 'revert') {
  try {
    const r = await api<any>(`/drift/${attr.id}/action`, { body: { action } })
    if (action === 'revert') return router.push('/runs/' + r.run.id)
    preview.value = { attr, action, diff: r.diff, sha: r.sha, file: r.file, error: '' }
  } catch (e) {
    preview.value = { attr, action, diff: '', sha: '', file: '', error: e instanceof ApiError ? e.message : String(e) }
  }
}
async function applyPreview() {
  const p = preview.value!
  try {
    await api(`/drift/${p.attr.id}/action`, { body: { action: p.action, apply: true, sha: p.sha } })
    preview.value = null
    router.push(codeHref(p.file, 1))
  } catch (e) { p.error = e instanceof Error ? e.message : String(e) }
}
</script>

<template>
  <AppShell>
    <header class="ghead">
      <h1>Graph</h1>
      <div class="seg-group" role="group" aria-label="Graph type">
        <button class="seg" :class="{ on: mode === 'resources' }" type="button" :aria-pressed="mode === 'resources'" @click="mode = 'resources'">Resources</button>
        <button class="seg" :class="{ on: mode === 'modules' }" type="button" :aria-pressed="mode === 'modules'" @click="mode = 'modules'">Modules</button>
        <button class="seg" :class="{ on: mode === 'system' }" type="button" :aria-pressed="mode === 'system'" title="Servers with the Ansible roles and ports on them" @click="mode = 'system'">System</button>
      </div>
      <label class="stack">Stack
        <select v-model="stackKey" class="sel" aria-label="Stack">
          <option v-for="s in stacks" :key="s.root + s.env" :value="`${s.root}|${s.env}`">{{ s.env }} / {{ s.root }}</option>
        </select>
      </label>
      <button class="btn sm drift-btn" type="button" :disabled="starting || !!checking || !target.root" :aria-busy="!!checking || undefined"
        title="Runs terraform plan -refresh-only (read-only)" @click="detectDrift">{{ checking ? `Checking ${checking.target.env}…` : 'Detect drift' }}</button>
    </header>
    <p v-if="error" class="pad err" role="alert">{{ error }}</p>
    <p v-else-if="!stacks.length" class="pad muted">No Terraform roots are configured.</p>
    <div v-else-if="mode === 'system'" class="body">
      <SystemPanel :root="target.root" :env="target.env" />
    </div>
    <div v-else class="body split">
      <section class="gpanel" aria-label="Dependency graph">
        <div class="gbar">
          <span class="mono small muted">{{ model?.nodes.length ?? 0 }} resources · {{ model?.edges.length ?? 0 }} edges · from <span class="hl">{{ model?.source ?? '…' }}</span><template v-if="drift.length"> + drift</template>
            <template v-if="checking"> · <RouterLink :to="`/runs/${checking.id}`">checking {{ checking.target.env }}…</RouterLink></template>
            <template v-else-if="lastCheck"> · <RouterLink :to="`/runs/${lastCheck.id}`" :title="`Drift check #${lastCheck.id}`">{{ target.env }} checked {{ since(lastCheck.ended_at ?? lastCheck.created_at, now) }}</RouterLink></template>
            <template v-if="schedule?.next"> · next {{ fmtTime(schedule.next) }}</template></span>
          <div class="tools"><DropMenu label="Export"><template #button>⋯</template><button v-for="it in exports" :key="it.label" type="button" role="menuitem" @click="it.run()">{{ it.label }}</button></DropMenu></div>
        </div>
        <div v-if="mode === 'resources'" class="findbar">
          <label class="find">
            <span class="sr-only">Find a resource</span>
            <input ref="findInput" v-model="query" type="search" class="finput mono" placeholder="Find a resource" aria-keyshortcuts="Control+F Meta+F"
              @keydown.enter.prevent="findNext" @keydown.esc="query = ''" />
            <kbd v-if="!query" class="kbd" aria-hidden="true">{{ isMac ? '⌘F' : 'Ctrl F' }}</kbd>
          </label>
          <div class="chips" role="group" aria-label="Filter">
            <button v-for="f in FILTERS" :key="f.id" class="fchip" :class="{ on: filter === f.id }" type="button" :aria-pressed="filter === f.id" @click="filter = f.id">
              {{ f.label }} <span class="cnt">{{ counts[f.id] }}</span></button>
          </div>
          <span v-if="narrowed" class="small muted" role="status">{{ matches.length }} of {{ pool.length }} shown</span>
          <span v-if="only" class="small muted">Only what {{ short(selected) || 'the selection' }} reaches · <button class="link" type="button" @click="only = null">Show all</button></span>
        </div>
        <div class="cwrap">
          <GraphCanvas ref="canvas" :nodes="nodes" :edges="edges" :selected="selected" :reach="reachIds" :reach-edges="reachEdges" controls aria-label="Dependency graph" @select="select">
            <template #legend>
              <div class="lgd">
                <span><i class="lg create" />create</span><span><i class="lg update" />update</span><span><i class="lg replace" />replace / destroy</span><span><i class="lg drift" />drift</span>
              </div>
              <div v-if="planRun" class="ovl">Overlay: plan #{{ planRun }}<template v-if="planBrief"> · {{ STATUS_LABEL[planBrief.status] ?? planBrief.status }} · {{ since(planBrief.created_at, now) }}</template>
                · <RouterLink :to="`/runs/${planRun}/plan`">Review</RouterLink></div>
              <div v-else class="ovl">No plan is waiting for approval</div>
            </template>
          </GraphCanvas>
        </div>
        <p v-if="model?.errors?.length" class="small warn pad0">Some files have syntax errors and were read only partly: {{ model.errors.join(', ') }}</p>
      </section>

      <section v-if="node" class="inspector" :class="{ drifted: node.drift }" aria-label="Selected resource">
        <div class="meta">
          <span class="lbl">Selected</span>
          <span class="mono addr">{{ node.id }}<span v-if="node.count" class="cnt"> {{ node.count }}</span></span>
          <span class="small muted">
            <template v-if="planRun && !node.action">no change in plan #{{ planRun }} · </template><template v-if="node.module">{{ node.module }} · </template><RouterLink :to="codeHref(node.file, node.line)">{{ node.file }}:{{ node.line }}</RouterLink></span>
          <span v-if="node.drift && nodeDrift[0]" class="pill st-warn">drifted · detected {{ since(nodeDrift[0].detected_at, now) }}</span>
          <span v-else-if="node.action" class="pill st-run">{{ node.action }} in plan #{{ planRun }}</span>
          <div class="rel">
            <span class="lbl">Uses · {{ uses.length }}</span>
            <ul v-if="uses.length" class="refs mono"><li v-for="id in uses" :key="id"><button class="link" type="button" @click="show(id)">{{ id }}</button></li></ul>
            <span v-else class="small muted">nothing</span>
            <span class="lbl">Used by · {{ usedBy.length }} <span class="muted">(directly or not)</span></span>
            <ul v-if="usedBy.length" class="refs mono"><li v-for="id in usedBy" :key="id"><button class="link" type="button" @click="show(id)">{{ id }}</button></li></ul>
            <span v-else class="small muted">nothing</span>
          </div>
          <p v-if="riskText" class="small riskbox" role="status">{{ riskText }}</p>
          <div class="acts">
            <button class="btn sm" type="button" :disabled="!uses.length && !usedBy.length" @click="showOnly">Show only these</button>
            <RouterLink class="btn sm" :to="codeHref(node.file, node.line)">Open in Code</RouterLink>
          </div>
        </div>
        <div class="driftpane">
          <span class="lbl">Code vs. real infrastructure</span>
          <p v-if="!nodeDrift.length" class="small muted">No drift recorded for this resource. <button class="link" type="button" :disabled="!!checking" @click="detectDrift">Detect drift</button></p>
          <template v-for="d in nodeDrift" :key="d.address">
            <p v-if="d.action === 'delete'" class="small warn">{{ d.address }} was deleted outside Terraform. A plan will recreate it.
              <button class="btn sm" type="button" @click="act(d.attrs[0], 'revert')">Revert with apply</button></p>
            <div v-else class="scroll">
              <table class="mono dt">
                <thead><tr><th>attribute</th><th>in state / code</th><th>actual</th><th></th></tr></thead>
                <tbody>
                  <tr v-for="a in d.attrs" :key="a.id">
                    <td>{{ a.attr_path }}</td><td class="muted">{{ a.code }}</td><td class="warn">{{ a.actual }}</td>
                    <td class="acts">
                      <button class="btn xs" type="button" @click="act(a, 'copy')">Copy into code</button>
                      <button class="btn xs" type="button" @click="act(a, 'ignore')">Ignore</button>
                    </td>
                  </tr>
                </tbody>
              </table>
              <button class="btn sm primary" type="button" @click="act(d.attrs[0], 'revert')">Revert with apply</button>
            </div>
          </template>
        </div>
      </section>
      <p v-else class="muted small hint">{{ mode === 'resources' ? 'Select a resource to see what it uses, what uses it, its plan changes and drift.' : 'Switch to Resources to inspect a resource.' }}</p>
    </div>

    <Modal v-if="preview" labelledby="pv-title" wide @close="preview = null">
      <div class="card dlg">
        <h2 id="pv-title">{{ preview.action === 'copy' ? 'Copy the real value into code' : 'Ignore this attribute' }}</h2>
        <p v-if="preview.error" class="err small" role="alert">{{ preview.error }}</p>
        <template v-else>
          <span class="small muted">{{ preview.file }} — review the change; nothing is written until you apply it.</span>
          <div class="diffbox"><DiffView :diff="preview.diff" /></div>
        </template>
        <div class="dact">
          <button class="btn" type="button" @click="preview = null">Cancel</button>
          <button v-if="!preview.error" class="btn primary" type="button" @click="applyPreview">Apply change</button>
        </div>
      </div>
    </Modal>
  </AppShell>
</template>

<style scoped>
.ghead { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 14px 24px; border-bottom: 1px solid var(--line); }
h1 { margin: 0; font-size: 18px; font-weight: 600; } h2 { margin: 0; font-size: 15px; font-weight: 600; }
.small { font-size: 13px; } .pad { padding: 24px; margin: 0; } .pad0 { margin: 0; padding: 8px 14px; }
.seg-group { display: flex; gap: 2px; padding: 2px; border: 1px solid var(--line-strong); border-radius: 7px; background: var(--bg-card); }
.seg { font-size: 14px; height: 30px; padding: 0 12px; border: 0; border-radius: 5px; background: transparent; color: var(--text-muted); cursor: pointer; }
.seg.on { background: var(--line-strong); color: var(--text); }
.stack { display: flex; align-items: center; gap: 8px; font-size: 14px; color: var(--text-muted); }
.sel { height: 32px; background: var(--bg-card); color: var(--text); border: 1px solid var(--line-strong); border-radius: 6px; padding: 0 8px; font-size: 14px; }
.drift-btn { margin-left: auto; }
.body { padding: 24px; display: flex; flex-direction: column; gap: 20px; overflow: auto; }
.split { display: grid; grid-template-columns: minmax(0, 1fr); align-content: start; }
.gpanel { border: 1px solid var(--line); border-radius: 10px; overflow: hidden; min-width: 0; }
.gbar, .findbar { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 10px 14px; border-bottom: 1px solid var(--line); background: var(--bg-panel); }
.findbar { padding: 8px 14px; }
.hl { color: var(--text); }
.tools { margin-left: auto; display: flex; gap: 6px; }
.find { position: relative; display: flex; align-items: center; }
.finput { height: 30px; width: 230px; max-width: 100%; padding: 0 52px 0 10px; font-size: 13px; color: var(--text); background: var(--bg-card); border: 1px solid var(--line-strong); border-radius: 6px; }
.finput:focus { outline: 2px solid var(--accent); outline-offset: -1px; }
.kbd { position: absolute; right: 7px; font: 11px var(--font-mono); color: var(--text-faint); border: 1px solid var(--line-strong); border-radius: 4px; padding: 0 4px; pointer-events: none; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.fchip { height: 28px; padding: 0 10px; font-size: 13px; border: 1px solid var(--line-strong); border-radius: 14px; background: var(--bg-card); color: var(--text-muted); cursor: pointer; display: inline-flex; align-items: center; gap: 6px; }
.fchip.on { border-color: var(--accent-line); background: var(--accent-bg); color: var(--text); }
.fchip .cnt { font: 500 12px var(--font-mono); color: var(--text-label); }
.cwrap { height: 460px; }
.lgd { display: flex; flex-wrap: wrap; gap: 4px 12px; }
.lgd span { display: inline-flex; align-items: center; gap: 5px; }
.lg { width: 14px; height: 10px; border: 1px solid var(--line-hover); border-radius: 3px; display: inline-block; }
.lg.drift { border: 1px dashed var(--warn); } .lg.update { border-color: var(--running); } .lg.create { border-color: var(--ok); } .lg.replace { border-color: var(--fail); }
.ovl { margin-top: 5px; font-family: var(--font-mono); font-size: 11.5px; }
.inspector { border: 1px solid var(--line); border-radius: 10px; background: var(--bg-panel); display: flex; flex-wrap: wrap; min-width: 0; }
.inspector.drifted { border-color: var(--warn-line); }
.meta { flex: 1 1 300px; min-width: 0; padding: 18px 20px; display: flex; flex-direction: column; gap: 10px; border-right: 1px solid var(--line); }
.addr { font-size: 15px; font-weight: 700; overflow-wrap: anywhere; }
.addr .cnt { color: var(--text-label); font-weight: 400; }
.rel { display: flex; flex-direction: column; gap: 4px; }
.rel .lbl:not(:first-child) { margin-top: 6px; }
.refs { list-style: none; margin: 0; padding: 0; font-size: 13px; max-height: 168px; overflow: auto; }
.refs li { padding: 1px 0; overflow-wrap: anywhere; }
.riskbox { margin: 0; padding: 8px 10px; border: 1px solid var(--fail-line); border-radius: 6px; background: var(--fail-bg); color: var(--fail-strong); }
.driftpane { flex: 2 1 420px; min-width: 0; padding: 18px 20px; display: flex; flex-direction: column; gap: 12px; }
.scroll { overflow-x: auto; display: flex; flex-direction: column; gap: 10px; align-items: flex-start; }
.dt { width: 100%; min-width: 520px; border-collapse: collapse; font-size: 13.5px; }
.dt th { text-align: left; font-weight: 500; padding: 6px 8px 6px 0; color: var(--text-label); }
.dt td { border-top: 1px solid var(--line); padding: 8px 8px 8px 0; overflow-wrap: anywhere; vertical-align: top; }
.acts { display: flex; gap: 6px; flex-wrap: wrap; }
.btn.xs { height: 26px; font-size: 12px; padding: 0 8px; }
.pill { font: 500 12px var(--font-mono); padding: 3px 8px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; align-self: flex-start; }
.st-warn { color: var(--warn); background: color-mix(in srgb, var(--warn) 10%, transparent); } .st-run { color: var(--running); background: color-mix(in srgb, var(--running) 12%, transparent); }
.link { background: none; border: 0; color: var(--accent); cursor: pointer; padding: 0; font: inherit; text-align: left; }
.link:disabled { color: var(--text-disabled); cursor: default; }
.hint { margin: 0; }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
.dlg { width: min(720px, 100%); }
.diffbox { max-height: 360px; overflow: auto; border: 1px solid var(--line); border-radius: 8px; }
.dact { display: flex; justify-content: flex-end; gap: 8px; }
/* wide screens: the inspector sits beside the canvas */
@media (min-width: 1280px) {
  .split { grid-template-columns: minmax(0, 1fr) 380px; align-items: start; }
  .cwrap { height: 600px; }
  .split .inspector { flex-direction: column; flex-wrap: nowrap; max-height: calc(100vh - 140px); overflow: auto; position: sticky; top: 0; }
  .split .meta, .split .driftpane { flex: none; }
  .split .meta { border-right: 0; border-bottom: 1px solid var(--line); }
  .split .dt { min-width: 0; }
  .split .hint { padding: 18px 20px; border: 1px dashed var(--line-strong); border-radius: 10px; }
}
</style>
