<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import GraphCanvas from './GraphCanvas.vue'
import DropMenu from './DropMenu.vue'
import { api } from '../api/client'
import type { PortRow, SystemGraphResponse, SystemModel, SystemScope } from '../api/types'
import { since, useRuns } from '../stores/runs'
import { onEvent } from '../composables/events'
import { useNow } from '../composables/now'
import { buildSystemMap, isFlagged, portLabel, type MapHost } from '../composables/systemMap'

const props = defineProps<{ root: string; env: string }>()
const runs = useRuns()
const now = useNow(30_000)

const sys = ref<SystemModel | null>(null)
const scopes = ref<SystemScope[]>([])
const stateError = ref('')
const pulledAt = ref('')
const error = ref('')
const loading = ref(false)
let seq = 0
async function load(refresh = false) {
  if (!props.root) return
  const mine = ++seq
  loading.value = true
  const q = `root=${encodeURIComponent(props.root)}&env=${encodeURIComponent(props.env)}${refresh ? '&refresh=1' : ''}`
  try {
    const r = await api<SystemGraphResponse>('/graph/system?' + q)
    if (mine !== seq) return
    sys.value = r.system
    scopes.value = r.scopes
    stateError.value = r.state_error
    pulledAt.value = r.state_pulled_at ?? ''
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
const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`
const menu = computed(() => [
  { label: loading.value ? 'Refreshing state…' : 'Refresh state', run: () => load(true), disabled: loading.value },
  { label: 'Export SVG', run: () => canvas.value?.exportSVG(`system-${props.env}.svg`) },
  { label: 'Export PNG', run: () => canvas.value?.exportPNG(`system-${props.env}.png`) },
])
const canCheck = computed(() => !checking.value.length && scopes.value.some((s) => s.hosts))
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
const flagged = (l: PortRow[]) => l.filter(isFlagged).length
// the "N port issues" chip scrolls to the first flagged row and marks it for a moment
const table = ref<HTMLElement>()
const jumped = ref('')
function jumpToIssue() {
  const p = picked.value?.host.ports.find(isFlagged)
  if (!p) return
  jumped.value = p.port + p.proto
  nextTick(() => table.value?.querySelector('tr.flag')?.scrollIntoView({ block: 'nearest', behavior: 'smooth' }))
  window.setTimeout(() => (jumped.value = ''), 1600)
}
watch(selected, () => (jumped.value = ''))
const allowedText = (p: PortRow, h: MapHost) =>
  p.allowed.join(', ') || (h.linked?.open_all ? 'every port is open' : p.flag === 'blocked' ? 'no firewall rule allows it' : h.linked ? '—' : 'unknown (not placed)')
</script>

<template>
  <section class="gpanel" aria-label="System architecture">
    <div class="gbar">
      <span class="mono small muted">{{ plural(map.hosts.length, 'host') }} · {{ plural(map.groups, 'group') }} · {{ placed }} on Terraform servers
        · {{ plural(map.services, 'service') }}<template v-if="map.issues"> · <span class="warn">{{ plural(map.issues, 'port issue') }}</span></template>
        · <template v-if="stateError"><span class="hl">code only</span></template><template v-else-if="pulledAt">state pulled {{ since(pulledAt, now) }}</template><template v-else>from <span class="hl">terraform state</span></template>
        <template v-if="map.listenAt"> · listening checked {{ since(map.listenAt, now) }}</template></span>
      <div class="tools">
        <button class="btn sm" type="button" :disabled="!canCheck" title="Runs community.general.listen_ports_facts on every host (read-only)" @click="checkPorts">
          {{ checking.length ? 'Checking ports…' : 'Check listening ports' }}</button>
        <DropMenu label="More actions"><template #button>⋯</template><button v-for="it in menu" :key="it.label" type="button" role="menuitem" :disabled="it.disabled" @click="it.run()">{{ it.label }}</button></DropMenu>
      </div>
    </div>
    <p v-if="error" class="small err pad0" role="alert">{{ error }}</p>
    <p v-for="(p, i) in portIssues" :key="i" class="small warnbar" role="status">
      Ports check (<RouterLink :to="`/runs/${p.run}`">run #{{ p.run }}</RouterLink>) failed<template v-if="p.hosts.length"> on {{ p.hosts.join(', ') }}</template>: {{ p.msg }}<template v-if="/netstat|\bss\b/.test(p.msg)"> — install <span class="mono">iproute2</span> (ss) or <span class="mono">net-tools</span> on those hosts.</template>
    </p>
    <p v-if="stateError" class="small warnbar" role="status">Hosts can't be matched to Terraform servers: {{ stateError }}.</p>
    <p v-if="noHosts" class="small muted pad0">No Ansible inventory is configured for <span class="mono">{{ env }}</span>, so there are no hosts to draw. Inventories pair with a stack by environment name; the Resources view shows the Terraform resources.</p>
    <p v-for="s in scopes.filter((x) => x.error)" :key="s.project" class="small err pad0">Inventory {{ s.project }} / {{ s.env }}: {{ s.error }}</p>
    <div class="cwrap"><GraphCanvas ref="canvas" :nodes="map.nodes" :edges="[]" :containers="map.containers" :selected="selected" :highlight="copies" packed controls :node-width="268" aria-label="System architecture" @select="select" /></div>
    <div class="legend small">
      <span><i class="dot ok" /> listening and allowed</span>
      <span><i class="dot warn" /> blocked or not listening</span>
      <span><i class="dot idle" /> not checked yet</span>
      <span><span class="card dashed">svc</span> found listening, no role declares it</span>
      <span><b class="star">*</b> in more than one group, drawn in each</span>
      <span><span class="chip off">9000</span> open, but nothing uses it</span>
    </div>
  </section>

  <section v-if="picked" class="inspector" aria-label="Selected host">
    <article class="host">
      <header class="hhead">
        <RouterLink class="mono hname" :to="inventoryHref(picked.host)">{{ picked.host.name }}</RouterLink>
        <span class="mono small muted">{{ picked.host.address }}<template v-if="picked.linked"> · {{ picked.linked.instance }} · matched by {{ picked.linked.matched_by }}</template></span>
        <span v-if="picked.host.os" class="small muted">{{ picked.host.os }}</span>
        <button v-if="flagged(picked.host.ports)" class="pill st-warn jump" type="button" title="Show the first port with an issue" @click="jumpToIssue">
          {{ flagged(picked.host.ports) }} port issue{{ flagged(picked.host.ports) === 1 ? '' : 's' }} ↓</button>
      </header>
      <div class="kv mono">
        <span>groups</span><span class="v">{{ groupNames(picked.host) }}</span>
        <span>roles</span>
        <span class="v"><template v-if="picked.host.roles.length"><span v-for="(r, i) in picked.host.roles" :key="r.name" :title="'from ' + r.playbooks.join(', ')">{{ r.name }}{{ i < picked.host.roles.length - 1 ? ', ' : '' }}</span></template><template v-else>— (no playbook targets this host)</template></span>
        <template v-if="picked.linked"><span>firewalls</span><span class="v">{{ picked.linked.firewalls.join(', ') || '— (none found in state)' }}<template v-if="picked.linked.open_all"> · a rule opens every port</template></span></template>
        <template v-else><span>server</span><span class="v warn">not placed: {{ picked.reason }}</span></template>
        <span>listening</span><span class="v">{{ picked.host.listen_at ? `checked ${since(picked.host.listen_at, now)}` : 'not checked yet' }}</span>
      </div>
      <div ref="table" class="scroll">
        <table class="mono dt">
          <thead><tr><th>port</th><th>used by</th><th>declared by</th><th>allowed by</th><th>listening</th><th>note</th></tr></thead>
          <tbody>
            <tr v-for="p in picked.host.ports" :key="p.port + p.proto" :class="{ flag: isFlagged(p), hit: jumped === p.port + p.proto }">
              <td>{{ portLabel(p) }}</td>
              <td>{{ p.services?.join(', ') || '—' }}</td>
              <td>{{ p.declared.join(', ') || '—' }}</td>
              <td :class="{ warn: p.flag === 'blocked' }">{{ allowedText(p, picked) }}</td>
              <td :class="{ warn: p.flag === 'not-listening' }">
                <template v-if="p.listening.length">{{ p.listening.join(', ') }}</template>
                <template v-else-if="p.flag === 'not-listening'">nothing listens on it</template>
                <template v-else-if="picked.host.listen_at">no</template>
                <template v-else><span class="muted">not checked</span> · <button class="link" type="button" :disabled="!canCheck" @click="checkPorts">{{ checking.length ? 'checking…' : 'Check' }}</button></template>
              </td>
              <td class="muted">{{ p.flag === 'undeclared' || p.flag === 'unused' ? flagText[p.flag] : '' }}</td>
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
.tools { margin-left: auto; display: flex; gap: 6px; }
.warn { color: var(--warn); }
.cwrap { height: 560px; }
.small { font-size: 13px; } .pad0 { margin: 0; padding: 8px 14px; }
.warnbar { margin: 0; padding: 6px 14px; background: var(--warn-bg); color: var(--warn-strong); border-bottom: 1px solid var(--line); }
.legend { display: flex; flex-wrap: wrap; gap: 6px 18px; padding: 8px 14px; border-top: 1px solid var(--line); color: var(--text-muted); background: var(--bg-panel); }
.legend > span { display: inline-flex; align-items: center; gap: 6px; }
.star { color: var(--accent); font-family: var(--font-mono); }
.chip { font: 500 11px var(--font-mono); padding: 0 5px; border-radius: 3px; border: 1px solid var(--line-strong); background: var(--bg-card); color: var(--text); }
.dot { width: 8px; height: 8px; border-radius: 50%; display: inline-block; background: var(--text-faint); }
.dot.ok { background: var(--ok); } .dot.warn { background: var(--warn); }
.card { font: 500 11px var(--font-mono); padding: 0 6px; border-radius: 4px; border: 1px solid var(--line-strong); background: var(--bg-card); color: var(--text); }
.card.dashed { border-style: dashed; }
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
.dt td.warn { color: var(--warn); }
.dt tr.hit td { background: var(--warn-bg); transition: background .3s; }
.jump { border: 0; cursor: pointer; }
.link { background: none; border: 0; color: var(--accent); cursor: pointer; padding: 0; font: inherit; }
.link:disabled { color: var(--text-disabled); cursor: default; }
.pill { font: 500 12px var(--font-mono); padding: 3px 8px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; }
.st-warn { color: var(--warn); background: color-mix(in srgb, var(--warn) 10%, transparent); }
</style>
