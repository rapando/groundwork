<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import GraphCanvas, { type GNode, type GNodeRow } from './GraphCanvas.vue'
import { api } from '../api/client'
import type { FirewallRule, PortRow, SystemHost, SystemModel, SystemScope } from '../api/types'
import { ago, useRuns } from '../stores/runs'
import { onEvent } from '../composables/events'
import { useNow } from '../composables/now'

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
const uniq = <T,>(l: T[]) => [...new Set(l)]
const portLabel = (p: { port: number; proto: string }) => (p.proto === 'tcp' ? String(p.port) : `${p.port}/${p.proto}`)
const ruleLabel = (r: FirewallRule) => {
  const ports = r.from <= 1 && r.to >= 65535 ? 'all ports' : r.from === r.to ? String(r.from) : `${r.from}-${r.to}`
  return `${r.proto === 'all' ? '' : r.proto + ' '}${ports}${r.sources?.length ? ' ← ' + r.sources.join(', ') : ''}`
}
function serverRows(hosts: SystemHost[]): GNodeRow[] {
  const rows: GNodeRow[] = [{ text: (hosts.length === 1 ? 'host ' : `${hosts.length} hosts `) + hosts.map((h) => h.name).join(', '), tone: 'muted' }]
  const roles = uniq(hosts.flatMap((h) => h.roles.map((r) => r.name)))
  rows.push({ text: roles.length ? 'roles ' + roles.join(', ') : 'no roles', tone: roles.length ? undefined : 'muted' })
  const used = hosts.flatMap((h) => h.ports).filter((p) => p.declared.length || p.listening.length)
  const ports = uniq(used.map(portLabel)).sort((a, b) => parseInt(a) - parseInt(b))
  if (ports.length) rows.push({ text: 'ports ' + ports.join(' ') })
  const open = uniq(hosts.flatMap((h) => h.ports).filter((p) => p.flag === 'unused').map(portLabel)).filter((p) => !ports.includes(p))
  if (open.length) rows.push({ text: 'open ' + open.join(' '), tone: 'muted' })
  const flagged = new Map<string, string[]>()
  for (const p of hosts.flatMap((h) => h.ports)) if (p.flag === 'blocked' || p.flag === 'not-listening') flagged.set(p.flag, uniq([...(flagged.get(p.flag) ?? []), portLabel(p)]))
  for (const [f, l] of flagged) rows.push({ text: `⚠ ${f === 'blocked' ? 'blocked' : 'not listening'} ${l.join(' ')}`, tone: 'warn' })
  return rows
}
const nodes = computed<GNode[]>(() => (sys.value?.nodes ?? []).map((n) => {
  const hosts = sys.value!.servers[n.id]
  const rules = sys.value!.rules[n.id]
  let rows: GNodeRow[] | undefined
  if (hosts?.length) rows = serverRows(hosts)
  else if (rules?.length) rows = [...rules.slice(0, 3).map((r) => ({ text: ruleLabel(r) })), ...(rules.length > 3 ? [{ text: `+${rules.length - 3} more`, tone: 'muted' as const }] : [])]
  return {
    id: n.id, title: (n.kind === 'data' ? 'data.' : '') + n.type, label: n.name, count: n.count,
    state: n.drift ? 'drift' : n.action ?? '', errors: n.errors, rows, dim: !!Object.keys(sys.value!.servers).length && !hosts && !rules,
  }
}))
const canvas = ref<InstanceType<typeof GraphCanvas>>()
defineExpose({ canvas })

const linked = computed(() => Object.values(sys.value?.servers ?? {}).reduce((n, l) => n + l.length, 0))
const noHosts = computed(() => !scopes.value.length)

// ---- inspector ----
const selected = ref('')
const node = computed(() => sys.value?.nodes.find((n) => n.id === selected.value))
const hosts = computed(() => sys.value?.servers[selected.value] ?? [])
const rules = computed(() => sys.value?.rules[selected.value] ?? [])
const codeHref = (f: string, l: number) => `/code/${f.split('/').map(encodeURIComponent).join('/')}?line=${l}`
const inventoryHref = (h: SystemHost) => ({ path: '/inventory', query: { project: h.project, env: h.env, host: h.name } })
const flagged = (l: PortRow[]) => l.filter((p) => p.flag === 'blocked' || p.flag === 'not-listening').length
</script>

<template>
  <section class="gpanel" aria-label="System architecture">
    <div class="gbar">
      <span class="mono small muted">{{ Object.keys(sys?.servers ?? {}).length }} servers · {{ linked }} hosts placed<template v-if="sys?.unlinked.length"> · {{ sys.unlinked.length }} not placed</template>
        · from <span class="hl">{{ stateError ? 'code only' : 'terraform state' }}</span><template v-if="scopes.length"> + {{ scopes.map((s) => s.project === '.' ? 'inventory' : s.project).join(', ') }}</template></span>
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
    <p v-if="stateError" class="small warnbar" role="status">Hosts can't be placed on servers: {{ stateError }}.</p>
    <p v-if="noHosts" class="small muted pad0">No Ansible inventory is configured for <span class="mono">{{ env }}</span>; servers are shown without software. Inventories pair with a stack by environment name.</p>
    <p v-for="s in scopes.filter((x) => x.error)" :key="s.project" class="small err pad0">Inventory {{ s.project }} / {{ s.env }}: {{ s.error }}</p>
    <div class="cwrap"><GraphCanvas ref="canvas" :nodes="nodes" :edges="sys?.edges ?? []" :containers="sys?.containers ?? []" :selected="selected" direction="DOWN" aria-label="System architecture" @select="selected = $event" /></div>
    <div v-if="sys?.unlinked.length" class="tray small">
      <span class="lbl">Hosts not placed</span>
      <ul>
        <li v-for="u in sys.unlinked" :key="u.project + u.name"><span class="mono">{{ u.name }}</span> <span class="muted mono">{{ u.address }}</span> — {{ u.reason }}</li>
      </ul>
    </div>
  </section>

  <section v-if="node" class="inspector" aria-label="Selected resource">
    <div class="meta">
      <span class="lbl">Selected</span>
      <span class="mono addr">{{ node.id }}</span>
      <div class="kv mono">
        <span>defined</span><RouterLink :to="codeHref(node.file, node.line)">{{ node.file }}:{{ node.line }}</RouterLink>
        <template v-if="node.count"><span>instances</span><span class="v">{{ node.count }}</span></template>
        <template v-if="hosts.length"><span>hosts</span><span class="v">{{ hosts.length }}</span></template>
      </div>
      <template v-if="rules.length">
        <span class="lbl">Inbound rules</span>
        <ul class="mono small rules"><li v-for="(r, i) in rules" :key="i">{{ ruleLabel(r) }}</li></ul>
      </template>
      <p v-if="!hosts.length && !rules.length" class="small muted">No Ansible host runs on this resource.</p>
    </div>
    <div v-if="hosts.length" class="hostpane">
      <article v-for="h in hosts" :key="h.project + h.name" class="host">
        <header class="hhead">
          <RouterLink class="mono hname" :to="inventoryHref(h)">{{ h.name }}</RouterLink>
          <span class="mono small muted">{{ h.instance }} · matched by {{ h.matched_by }}</span>
          <span v-if="h.os" class="small muted">{{ h.os }}</span>
          <span v-if="flagged(h.ports)" class="pill st-warn">{{ flagged(h.ports) }} port issue{{ flagged(h.ports) === 1 ? '' : 's' }}</span>
        </header>
        <div class="kv mono">
          <span>roles</span>
          <span class="v"><template v-if="h.roles.length"><span v-for="(r, i) in h.roles" :key="r.name" :title="'from ' + r.playbooks.join(', ')">{{ r.name }}{{ i < h.roles.length - 1 ? ', ' : '' }}</span></template><template v-else>— (no playbook targets this host)</template></span>
          <span>firewalls</span><span class="v">{{ h.firewalls.join(', ') || '— (none found in state)' }}<template v-if="h.open_all"> · a rule opens every port</template></span>
          <span>listening</span><span class="v">{{ h.listen_at ? `checked ${ago(h.listen_at, now)} ago` : 'not checked yet' }}</span>
        </div>
        <div class="scroll">
          <table class="mono dt">
            <thead><tr><th>port</th><th>declared by</th><th>allowed by</th><th>listening</th><th></th></tr></thead>
            <tbody>
              <tr v-for="p in h.ports" :key="p.port + p.proto" :class="{ flag: p.flag === 'blocked' || p.flag === 'not-listening' }">
                <td>{{ portLabel(p) }}</td>
                <td>{{ p.declared.join(', ') || '—' }}</td>
                <td>{{ p.allowed.join(', ') || (h.open_all ? 'every port is open' : '—') }}</td>
                <td>{{ p.listening.join(', ') || (h.listen_at ? 'no' : '?') }}</td>
                <td :class="p.flag === 'blocked' || p.flag === 'not-listening' ? 'warn' : 'muted'">{{ p.flag ? flagText[p.flag] : '' }}</td>
              </tr>
              <tr v-if="!h.ports.length"><td colspan="5" class="muted">No ports declared, allowed or seen listening.</td></tr>
            </tbody>
          </table>
        </div>
      </article>
    </div>
  </section>
  <p v-else class="muted small pad0">Select a server to see the hosts on it, their roles and ports.</p>
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
.tray { padding: 8px 14px; border-top: 1px solid var(--line); color: var(--text-muted); }
.tray ul { margin: 4px 0 0; padding-left: 18px; }
.inspector { border: 1px solid var(--line); border-radius: 10px; background: var(--bg-panel); display: flex; flex-wrap: wrap; }
.meta { flex: 1 1 260px; padding: 18px 20px; display: flex; flex-direction: column; gap: 10px; border-right: 1px solid var(--line); }
.addr { font-size: 15px; font-weight: 700; overflow-wrap: anywhere; }
.kv { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 6px 14px; font-size: 13px; color: var(--text-muted); }
.kv .v { color: var(--text); overflow-wrap: anywhere; }
.rules { margin: 0; padding-left: 16px; }
.hostpane { flex: 3 1 480px; min-width: 0; padding: 18px 20px; display: flex; flex-direction: column; gap: 18px; }
.host { display: flex; flex-direction: column; gap: 10px; }
.host + .host { border-top: 1px solid var(--line); padding-top: 16px; }
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
