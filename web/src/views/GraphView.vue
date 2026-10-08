<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from '../components/AppShell.vue'
import GraphCanvas, { type GEdge, type GNode } from '../components/GraphCanvas.vue'
import DiffView from '../components/DiffView.vue'
import { api, ApiError } from '../api/client'
import type { DriftAttr, DriftResource, GraphModel } from '../api/types'
import { ago, useRuns } from '../stores/runs'
import { useWorkspace } from '../stores/workspace'
import { onEvent } from '../composables/events'
import { useNow } from '../composables/now'

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
const mode = ref<'resources' | 'modules'>('resources')

const model = ref<GraphModel | null>(null)
const planRun = ref<number | null>(null)
const drift = ref<DriftResource[]>([])
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
onMounted(load)
watch(stackKey, () => { selected.value = ''; load() })
onEvent('drift.updated', load)
onEvent('run.updated', (d: { kind: string; status: string }) => { if (d.kind === 'tf.plan' && d.status === 'waiting_approval') load() })

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
  return m.nodes.map((n) => ({
    id: n.id, title: (n.kind === 'data' ? 'data.' : '') + n.type, label: n.name + (n.module ? '' : ''), count: n.count,
    state: n.drift ? 'drift' : n.action ?? '', errors: n.errors,
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
const canvas = ref<InstanceType<typeof GraphCanvas>>()

// ---- inspector ----
const selected = ref('')
const node = computed(() => model.value?.nodes.find((n) => n.id === selected.value))
const dependsOn = computed(() => (model.value?.edges ?? []).filter((e) => e.to === selected.value).map((e) => e.from))
const usedBy = computed(() => (model.value?.edges ?? []).filter((e) => e.from === selected.value).map((e) => e.to))
const nodeDrift = computed(() => drift.value.filter((d) => d.address === selected.value || d.address.replace(/\[[^\]]*\]/g, '') === selected.value))
const codeHref = (f: string, l: number) => `/code/${f.split('/').map(encodeURIComponent).join('/')}?line=${l}`

// ---- drift actions ----
const detecting = ref(false)
async function detectDrift() {
  detecting.value = true
  try {
    const r = await runs.submit({ kind: 'tf.drift', root: target.value.root, env: target.value.env })
    router.push('/runs/' + r.id)
  } catch (e) { error.value = e instanceof Error ? e.message : String(e) } finally { detecting.value = false }
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
      </div>
      <label class="stack">Stack
        <select v-model="stackKey" class="sel" aria-label="Stack">
          <option v-for="s in stacks" :key="s.root + s.env" :value="`${s.root}|${s.env}`">{{ s.env }} / {{ s.root }}</option>
        </select>
      </label>
      <div class="legend">
        <span><i class="lg" />unchanged</span><span><i class="lg drift" />drift</span><span><i class="lg update" />~ update</span>
        <span><i class="lg create" />+ create</span><span><i class="lg replace" />± replace / destroy</span>
      </div>
      <button class="btn sm" type="button" :disabled="detecting || !target.root" @click="detectDrift">Detect drift</button>
    </header>
    <p v-if="error" class="pad err" role="alert">{{ error }}</p>
    <p v-else-if="!stacks.length" class="pad muted">No Terraform roots are configured.</p>
    <div v-else class="body">
      <section class="gpanel" aria-label="Dependency graph">
        <div class="gbar">
          <span class="mono small muted">{{ model?.nodes.length ?? 0 }} resources · {{ model?.edges.length ?? 0 }} edges · from <span class="hl">{{ model?.source ?? '…' }}</span><template v-if="planRun"> + <RouterLink :to="`/runs/${planRun}/plan`">plan #{{ planRun }}</RouterLink></template><template v-if="drift.length"> + drift</template></span>
          <div class="zoom">
            <button class="btn sq" type="button" aria-label="Zoom in" @click="canvas?.zoom(1.2)">+</button>
            <button class="btn sq" type="button" aria-label="Zoom out" @click="canvas?.zoom(1 / 1.2)">−</button>
            <button class="btn sm" type="button" @click="canvas?.fit()">Fit</button>
            <button class="btn sm" type="button" @click="canvas?.exportSVG(`graph-${target.env}.svg`)">Export SVG</button>
            <button class="btn sm" type="button" @click="canvas?.exportPNG(`graph-${target.env}.png`)">PNG</button>
          </div>
        </div>
        <div class="cwrap"><GraphCanvas ref="canvas" :nodes="nodes" :edges="edges" :selected="selected" aria-label="Dependency graph" @select="selected = mode === 'resources' ? $event : ''" /></div>
        <p v-if="model?.errors?.length" class="small warn pad0">Some files have syntax errors and were read only partly: {{ model.errors.join(', ') }}</p>
      </section>

      <section v-if="node" class="inspector" :class="{ drifted: node.drift }" aria-label="Selected resource">
        <div class="meta">
          <span class="lbl">Selected</span>
          <span class="mono addr">{{ node.id }}</span>
          <span v-if="node.drift && nodeDrift[0]" class="pill st-warn">drifted · detected {{ ago(nodeDrift[0].detected_at, now) }} ago</span>
          <span v-else-if="node.action" class="pill st-run">{{ node.action }} in plan #{{ planRun }}</span>
          <div class="kv mono">
            <span>module</span><span class="v">{{ node.module || '(root)' }}</span>
            <span>defined</span><RouterLink :to="codeHref(node.file, node.line)">{{ node.file }}:{{ node.line }}</RouterLink>
            <template v-if="node.count"><span>instances</span><span class="v">{{ node.count }}</span></template>
            <span>depends on</span><span class="v">{{ dependsOn.map(short).join(', ') || '—' }}</span>
            <span>used by</span><span class="v">{{ usedBy.map(short).join(', ') || '—' }}</span>
          </div>
        </div>
        <div class="driftpane">
          <span class="lbl">Code vs. real infrastructure</span>
          <p v-if="!nodeDrift.length" class="small muted">No drift recorded for this resource. <button class="link" type="button" @click="detectDrift">Detect drift</button></p>
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
      <p v-else class="muted small pad0">Select a resource to inspect it.</p>
    </div>

    <div v-if="preview" class="modal" role="dialog" aria-modal="true" aria-labelledby="pv-title" @keydown.esc="preview = null">
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
    </div>
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
.legend { margin-left: auto; display: flex; flex-wrap: wrap; gap: 14px; font-size: 13px; color: var(--text-muted); }
.legend span { display: flex; align-items: center; gap: 6px; }
.lg { width: 14px; height: 10px; border: 1px solid var(--line-hover); border-radius: 3px; display: inline-block; }
.lg.drift { border: 1px dashed var(--warn); } .lg.update { border-color: var(--running); } .lg.create { border-color: var(--ok); } .lg.replace { border-color: var(--fail); }
.body { padding: 24px; display: flex; flex-direction: column; gap: 20px; overflow: auto; }
.gpanel { border: 1px solid var(--line); border-radius: 10px; overflow: hidden; }
.gbar { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 10px 14px; border-bottom: 1px solid var(--line); background: var(--bg-panel); }
.hl { color: var(--text); }
.zoom { margin-left: auto; display: flex; gap: 6px; }
.btn.sq { height: 30px; width: 30px; padding: 0; justify-content: center; }
.cwrap { height: 460px; }
.inspector { border: 1px solid var(--line); border-radius: 10px; background: var(--bg-panel); display: flex; flex-wrap: wrap; }
.inspector.drifted { border-color: var(--warn-line); }
.meta { flex: 1 1 300px; padding: 18px 20px; display: flex; flex-direction: column; gap: 10px; border-right: 1px solid var(--line); }
.addr { font-size: 15px; font-weight: 700; overflow-wrap: anywhere; }
.kv { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 6px 14px; font-size: 13px; color: var(--text-muted); }
.kv .v { color: var(--text); overflow-wrap: anywhere; }
.driftpane { flex: 2 1 420px; min-width: 0; padding: 18px 20px; display: flex; flex-direction: column; gap: 12px; }
.scroll { overflow-x: auto; display: flex; flex-direction: column; gap: 10px; align-items: flex-start; }
.dt { width: 100%; min-width: 520px; border-collapse: collapse; font-size: 13.5px; }
.dt th { text-align: left; font-weight: 500; padding: 6px 8px 6px 0; color: var(--text-label); }
.dt td { border-top: 1px solid var(--line); padding: 8px 8px 8px 0; overflow-wrap: anywhere; vertical-align: top; }
.acts { display: flex; gap: 6px; flex-wrap: wrap; }
.btn.xs { height: 26px; font-size: 12px; padding: 0 8px; }
.pill { font: 500 12px var(--font-mono); padding: 3px 8px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; align-self: flex-start; }
.st-warn { color: var(--warn); background: color-mix(in srgb, var(--warn) 10%, transparent); } .st-run { color: var(--running); background: color-mix(in srgb, var(--running) 12%, transparent); }
.link { background: none; border: 0; color: var(--accent); cursor: pointer; padding: 0; font: inherit; }
.modal { position: fixed; inset: 0; background: var(--scrim); display: grid; place-items: center; z-index: 50; padding: 16px; }
.dlg { width: min(720px, 100%); }
.diffbox { max-height: 360px; overflow: auto; border: 1px solid var(--line); border-radius: 8px; }
.dact { display: flex; justify-content: flex-end; gap: 8px; }
</style>
