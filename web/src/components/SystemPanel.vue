<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import GraphCanvas from './GraphCanvas.vue'
import { api } from '../api/client'
import type { PortRow, SystemModel, SystemScope } from '../api/types'
import { since, useRuns } from '../stores/runs'
import { onEvent } from '../composables/events'
import { useNow } from '../composables/now'
import { buildSystemMap, portLabel, type MapHost } from '../composables/systemMap'

const props = defineProps<{ root: string; env: string }>()
const runs = useRuns()
const now = useNow(30_000)

const sys = ref<SystemModel | null>(null)
const scopes = ref<SystemScope[]>([])
const stateError = ref('')
const error = ref('')
const loading = ref(false)
let seq = 0
async function load(refresh = false) {
  if (!props.root) return
  const mine = ++seq
  loading.value = true
  const q = `root=${encodeURIComponent(props.root)}&env=${encodeURIComponent(props.env)}${refresh ? '&refresh=1' : ''}`
  try {
    const r = await api<{ system: SystemModel; scopes: SystemScope[]; state_error: string }>('/graph/system?' + q)
    if (mine !== seq) return
    sys.value = r.system
    scopes.value = r.scopes
    stateError.value = r.state_error
    error.value = ''
  } catch (e) {
    if (mine === seq) error.value = e instanceof Error ? e.message : String(e)
  } finally { if (mine === seq) loading.value = false }
}
onMounted(() => load())
watch(() => [props.root, props.env], () => { selected.value = ''; load() })

// a ports check or an apply changes what is shown
const checking = ref<number[]>([])
// hosts a ports check failed on, grouped by message (e.g. no ss/netstat installed)
const portIssues = ref<{ run: number; msg: string; hosts: string[] }[]>([])
async function checkDone(id: number, status: string) {
  checking.value = checking.value.filter((x) => x !== id)
  load()
  if (status === 'succeeded') return
  try {
    const r = await api<{ results: { host: string; status: string; msg?: string }[] }>(`/runs/${id}/hosts`)
    const by = new Map<string, string[]>()
    for (const x of r.results) if (x.status === 'failed' || x.status === 'unreachable') by.set(x.msg || x.status, [...(by.get(x.msg || x.status) ?? []), x.host])
    portIssues.value.push(...[...by].map(([msg, hosts]) => ({ run: id, msg, hosts })))
    if (!by.size) portIssues.value.push({ run: id, msg: 'the check failed', hosts: [] })
  } catch (e) { error.value = e instanceof Error ? e.message : String(e) }
}
const offs = [
  onEvent('run.updated', (d: { id: number; kind: string; status: string }) => {
    const done = !['queued', 'running', 'waiting_approval'].includes(d.status)
    if (!done) return
    if (checking.value.includes(d.id)) checkDone(d.id, d.status)
    else if (d.kind === 'tf.apply' || d.kind === 'tf.plan') load(true)
  }),
]
onBeforeUnmount(() => offs.forEach((off) => off()))

async function checkPorts() {
  error.value = ''
  portIssues.value = []
  try {
    for (const s of scopes.value.filter((x) => !x.error && x.hosts)) {
      const r = await runs.submit({ kind: 'ans.adhoc', project: s.project, env: s.env, pattern: 'all', module: 'community.general.listen_ports_facts' })
      checking.value = [...checking.value, r.id]
    }
  } catch (e) { error.value = e instanceof Error ? e.message : String(e) }
}

// ---- canvas ----
const flagText: Record<string, string> = {
  blocked: 'declared, but no firewall rule allows it',
  'not-listening': 'declared, but nothing listens on it',
  undeclared: 'listening and open, but no variable declares it',
  unused: 'open, but nothing declares or listens on it',
}
const map = computed(() => buildSystemMap(sys.value))
const canvas = ref<InstanceType<typeof GraphCanvas>>()
defineExpose({ canvas })

const placed = computed(() => map.value.hosts.filter((h) => h.linked).length)
const noHosts = computed(() => !scopes.value.length)

// ---- inspector ----
// selecting a box selects the host: its copies in other groups light up too
const selected = ref('')
const selectedHost = computed(() => map.value.hostOf.get(selected.value) ?? '')
const copies = computed(() => [...map.value.hostOf].filter(([id, k]) => k === selectedHost.value && id !== selected.value).map(([id]) => id))
const picked = computed(() => map.value.hosts.find((h) => h.key === selectedHost.value))
function select(id: string) { selected.value = map.value.hostOf.has(id) && id !== selected.value ? id : '' }
const inventoryHref = (h: { project: string; env: string; name: string }) => ({ path: '/inventory', query: { project: h.project, env: h.env, host: h.name } })
const groupNames = (h: MapHost['host']) => h.groups?.map((g) => [...(g.path ?? []), g.name].join(' / ')).join(', ') || 'none'
const flagged = (l: PortRow[]) => l.filter((p) => p.flag === 'blocked' || p.flag === 'not-listening').length
</script>

<template>
  <section class="gpanel" aria-label="System architecture">
    <div class="gbar">
      <span class="mono small muted">{{ map.hosts.length }} host{{ map.hosts.length === 1 ? '' : 's' }} · {{ map.groups }} group{{ map.groups === 1 ? '' : 's' }} · {{ placed }} on Terraform servers
        · from <template v-if="scopes.length"><span class="hl">{{ scopes.map((s) => s.project === '.' ? 'inventory' : s.project).join(', ') }}</span> + </template><span class="hl">{{ stateError ? 'code only' : 'terraform state' }}</span></span>
      <div class="zoom">
        <button class="btn sm" type="button" :disabled="!!checking.length || !scopes.some((s) => s.hosts)" title="Runs community.general.listen_ports_facts on every host (read-only)" @click="checkPorts">
          {{ checking.length ? 'Checking ports…' : 'Check listening ports' }}</button>
        <button class="btn sm" type="button" :disabled="loading" @click="load(true)">Refresh state</button>
        <button class="btn sq" type="button" aria-label="Zoom in" @click="canvas?.zoom(1.2)">+</button>
        <button class="btn sq" type="button" aria-label="Zoom out" @click="canvas?.zoom(1 / 1.2)">−</button>
        <button class="btn sm" type="button" @click="canvas?.fit()">Fit</button>
        <button class="btn sm" type="button" @click="canvas?.exportSVG(`system-${env}.svg`)">Export SVG</button>
        <button class="btn sm" type="button" @click="canvas?.exportPNG(`system-${env}.png`)">PNG</button>
      </div>
    </div>
    <p v-if="error" class="small err pad0" role="alert">{{ error }}</p>
    <p v-for="(p, i) in portIssues" :key="i" class="small warnbar" role="status">
      Ports check (<RouterLink :to="`/runs/${p.run}`">run #{{ p.run }}</RouterLink>) failed<template v-if="p.hosts.length"> on {{ p.hosts.join(', ') }}</template>: {{ p.msg }}<template v-if="/netstat|\bss\b/.test(p.msg)"> — install <span class="mono">iproute2</span> (ss) or <span class="mono">net-tools</span> on those hosts.</template>
    </p>
    <p v-if="stateError" class="small warnbar" role="status">Hosts can't be matched to Terraform servers: {{ stateError }}.</p>
    <p v-if="noHosts" class="small muted pad0">No Ansible inventory is configured for <span class="mono">{{ env }}</span>, so there are no hosts to draw. Inventories pair with a stack by environment name; the Resources view shows the Terraform resources.</p>
    <p v-for="s in scopes.filter((x) => x.error)" :key="s.project" class="small err pad0">Inventory {{ s.project }} / {{ s.env }}: {{ s.error }}</p>
    <div class="cwrap"><GraphCanvas ref="canvas" :nodes="map.nodes" :edges="[]" :containers="map.containers" :selected="selected" :highlight="copies" packed :node-width="236" aria-label="System architecture" @select="select" /></div>
    <div class="legend small">
      <span><b class="star">*</b> listed in more than one group, drawn in each</span>
      <span><span class="chip">443</span> port the service declares or listens on</span>
      <span><span class="chip warn">8080</span> declared, but no firewall rule allows it or nothing listens</span>
      <span><span class="chip off">9000</span> open, but nothing uses it</span>
    </div>
  </section>

  <section v-if="picked" class="inspector" aria-label="Selected host">
    <article class="host">
      <header class="hhead">
        <RouterLink class="mono hname" :to="inventoryHref(picked.host)">{{ picked.host.name }}</RouterLink>
        <span class="mono small muted">{{ picked.host.address }}<template v-if="picked.linked"> · {{ picked.linked.instance }} · matched by {{ picked.linked.matched_by }}</template></span>
        <span v-if="picked.host.os" class="small muted">{{ picked.host.os }}</span>
        <span v-if="flagged(picked.host.ports)" class="pill st-warn">{{ flagged(picked.host.ports) }} port issue{{ flagged(picked.host.ports) === 1 ? '' : 's' }}</span>
      </header>
      <div class="kv mono">
        <span>groups</span><span class="v">{{ groupNames(picked.host) }}</span>
        <span>roles</span>
        <span class="v"><template v-if="picked.host.roles.length"><span v-for="(r, i) in picked.host.roles" :key="r.name" :title="'from ' + r.playbooks.join(', ')">{{ r.name }}{{ i < picked.host.roles.length - 1 ? ', ' : '' }}</span></template><template v-else>— (no playbook targets this host)</template></span>
        <template v-if="picked.linked"><span>firewalls</span><span class="v">{{ picked.linked.firewalls.join(', ') || '— (none found in state)' }}<template v-if="picked.linked.open_all"> · a rule opens every port</template></span></template>
        <template v-else><span>server</span><span class="v warn">not placed: {{ picked.reason }}</span></template>
        <span>listening</span><span class="v">{{ picked.host.listen_at ? `checked ${since(picked.host.listen_at, now)}` : 'not checked yet' }}</span>
      </div>
      <div class="scroll">
        <table class="mono dt">
          <thead><tr><th>port</th><th>used by</th><th>declared by</th><th>allowed by</th><th>listening</th><th></th></tr></thead>
          <tbody>
            <tr v-for="p in picked.host.ports" :key="p.port + p.proto" :class="{ flag: p.flag === 'blocked' || p.flag === 'not-listening' }">
              <td>{{ portLabel(p) }}</td>
              <td>{{ p.services?.join(', ') || '—' }}</td>
              <td>{{ p.declared.join(', ') || '—' }}</td>
              <td>{{ p.allowed.join(', ') || (picked.linked?.open_all ? 'every port is open' : picked.linked ? '—' : 'unknown (not placed)') }}</td>
              <td>{{ p.listening.join(', ') || (picked.host.listen_at ? 'no' : '?') }}</td>
              <td :class="p.flag === 'blocked' || p.flag === 'not-listening' ? 'warn' : 'muted'">{{ p.flag ? flagText[p.flag] : '' }}</td>
            </tr>
            <tr v-if="!picked.host.ports.length"><td colspan="6" class="muted">No ports declared, allowed or seen listening.</td></tr>
          </tbody>
        </table>
      </div>
    </article>
  </section>
  <p v-else class="muted small pad0">Select a host to see its groups, roles and ports.</p>
</template>

<style scoped>
.gpanel { flex-shrink: 0; border: 1px solid var(--line); border-radius: 10px; overflow: hidden; }
.gbar { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 10px 14px; border-bottom: 1px solid var(--line); background: var(--bg-panel); }
.hl { color: var(--text); }
.zoom { margin-left: auto; display: flex; flex-wrap: wrap; gap: 6px; }
.btn.sq { height: 30px; width: 30px; padding: 0; justify-content: center; }
.cwrap { height: 520px; }
.small { font-size: 13px; } .pad0 { margin: 0; padding: 8px 14px; }
.warnbar { margin: 0; padding: 6px 14px; background: var(--warn-bg); color: var(--warn-strong); border-bottom: 1px solid var(--line); }
.legend { display: flex; flex-wrap: wrap; gap: 6px 18px; padding: 8px 14px; border-top: 1px solid var(--line); color: var(--text-muted); background: var(--bg-panel); }
.legend > span { display: inline-flex; align-items: center; gap: 6px; }
.star { color: var(--accent); font-family: var(--font-mono); }
.chip { font: 500 11px var(--font-mono); padding: 0 5px; border-radius: 3px; border: 1px solid var(--line-strong); background: var(--bg-card); color: var(--text); }
.chip.warn { color: var(--warn); border-color: var(--warn-line); background: var(--warn-bg); }
.chip.off { color: var(--text-label); border-style: dashed; }
.v.warn { color: var(--warn); }
.inspector { border: 1px solid var(--line); border-radius: 10px; background: var(--bg-panel); padding: 18px 20px; }
.kv { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 6px 14px; font-size: 13px; color: var(--text-muted); }
.kv .v { color: var(--text); overflow-wrap: anywhere; }
.host { display: flex; flex-direction: column; gap: 10px; }
.hhead { display: flex; flex-wrap: wrap; align-items: baseline; gap: 10px; }
.hname { font-size: 14px; font-weight: 700; }
.scroll { overflow-x: auto; }
.dt { width: 100%; min-width: 560px; border-collapse: collapse; font-size: 13px; }
.dt th { text-align: left; font-weight: 500; padding: 6px 8px 6px 0; color: var(--text-label); }
.dt td { border-top: 1px solid var(--line); padding: 7px 8px 7px 0; overflow-wrap: anywhere; vertical-align: top; }
.dt tr.flag td:first-child { color: var(--warn); font-weight: 700; }
.pill { font: 500 12px var(--font-mono); padding: 3px 8px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; }
.st-warn { color: var(--warn); background: color-mix(in srgb, var(--warn) 10%, transparent); }
</style>
