<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from '../components/AppShell.vue'
import { api, ApiError } from '../api/client'
import type { HostDetail, InvHost, InventoryResponse } from '../api/types'
import { ago, useRuns } from '../stores/runs'
import { onEvent } from '../composables/events'
import { useNow } from '../composables/now'

const route = useRoute()
const router = useRouter()
const runs = useRuns()
const now = useNow(10_000)

const data = ref<InventoryResponse | null>(null)
const loading = ref(false)
const notice = ref('')
const group = ref('all')
const checked = reactive(new Set<string>())
const selected = ref('')
const detail = ref<HostDetail | null>(null)
const detailError = ref('')

const scope = computed(() => data.value?.scope)
async function load() {
  loading.value = true
  const q = new URLSearchParams()
  if (route.query.project) q.set('project', String(route.query.project))
  if (route.query.env) q.set('env', String(route.query.env))
  try {
    data.value = await api<InventoryResponse>('/inventory?' + q.toString())
    const want = String(route.query.host ?? '')
    if (want && data.value.hosts?.some((h) => h.name === want)) selectHost(want)
    else if (!selected.value && data.value.hosts?.length) selectHost(data.value.hosts[0].name)
    else if (selected.value) selectHost(selected.value)
  } finally { loading.value = false }
}
onMounted(load)
watch(() => [route.query.project, route.query.env], () => { selected.value = ''; detail.value = null; checked.clear(); group.value = 'all'; load() })

// refresh when an ansible job in this scope finishes (ping, facts, playbooks)
let t: number | undefined
onEvent('run.updated', (d: { kind: string; status: string }) => {
  if (!d.kind?.startsWith('ans.') || d.status === 'running' || d.status === 'queued') return
  window.clearTimeout(t)
  t = window.setTimeout(load, 300)
})

function setScope(project: string, env: string) {
  router.push({ path: '/inventory', query: { project, env } })
}
const scopeCounts = computed(() => (data.value?.scopes ?? []).map((s) => ({ ...s, current: s.project === scope.value?.project && s.env === scope.value?.env })))

const hosts = computed<InvHost[]>(() => (data.value?.hosts ?? []).filter((h) => group.value === 'all' || h.groups.includes(group.value)))
const allChecked = computed(() => hosts.value.length > 0 && hosts.value.every((h) => checked.has(h.name)))
function toggleAll(on: boolean) { hosts.value.forEach((h) => (on ? checked.add(h.name) : checked.delete(h.name))) }
const targetHosts = computed(() => [...checked])
const limit = computed(() => targetHosts.value.join(','))

async function selectHost(name: string) {
  selected.value = name
  detailError.value = ''
  const s = scope.value
  if (!s) return
  try {
    const d = await api<HostDetail>(`/inventory/host?project=${encodeURIComponent(s.project)}&env=${encodeURIComponent(s.env)}&host=${encodeURIComponent(name)}`)
    if (selected.value === name) detail.value = d
  } catch (e) { detailError.value = e instanceof Error ? e.message : String(e) }
}

function say(m: string) { notice.value = m; setTimeout(() => (notice.value = ''), 6000) }
async function quick(kind: 'ans.ping' | 'ans.facts') {
  const s = scope.value
  if (!s) return
  try {
    const r = await runs.submit({ kind, project: s.project, env: s.env, limit: limit.value })
    say(`${kind === 'ans.ping' ? 'Pinging' : 'Gathering facts from'} ${targetHosts.value.length || 'all'} host(s) · run #${r.id}`)
  } catch (e) { say(e instanceof Error ? e.message : String(e)) }
}

// ---- ad-hoc ----
const READ_ONLY = new Set(['ping', 'setup', 'gather_facts', 'stat', 'debug', 'package_facts', 'service_facts'])
const adhoc = reactive({ open: false, pattern: 'all', module: 'ping', args: '', confirm: '', error: '' })
const adhocMutating = computed(() => !READ_ONLY.has(adhoc.module.replace(/^ansible\.builtin\./, '')))
function openAdhoc() {
  Object.assign(adhoc, { open: true, pattern: limit.value || 'all', module: 'ping', args: '', confirm: '', error: '' })
}
async function runAdhoc() {
  const s = scope.value!
  adhoc.error = ''
  try {
    const r = await runs.submit({ kind: 'ans.adhoc', project: s.project, env: s.env, pattern: adhoc.pattern, module: adhoc.module, args: adhoc.args, confirm_text: adhoc.confirm })
    router.push('/runs/' + r.id)
  } catch (e) { adhoc.error = e instanceof ApiError ? e.message : String(e) }
}

// ---- playbook ----
const pb = reactive({ open: false, playbook: '', mode: 'check' as 'check' | 'run', tags: '', error: '' })
function openPlaybook() {
  Object.assign(pb, { open: true, playbook: data.value?.playbooks?.[0] ?? '', mode: 'check', tags: '', error: '' })
}
async function runPlaybook() {
  const s = scope.value!
  pb.error = ''
  try {
    const r = await runs.submit({ kind: pb.mode === 'check' ? 'ans.check' : 'ans.playbook', project: s.project, env: s.env, playbook: pb.playbook, limit: limit.value, tags: pb.tags })
    router.push('/runs/' + r.id)
  } catch (e) { pb.error = e instanceof Error ? e.message : String(e) }
}

const reach = (h: InvHost) => {
  if (!h.reachable) return { cls: 'idle', text: 'unknown' }
  return h.reachable.reachable ? { cls: 'ok', text: `up · ${h.reachable.latency_ms}ms` } : { cls: 'fail', text: 'unreachable' }
}
const play = (h: InvHost) => {
  const p = h.last_play
  if (!p) return ''
  if (p.unreachable) return `#${p.run_id} unreachable`
  if (p.failures) return `#${p.run_id} failed`
  return `#${p.run_id}${p.phase === 'check' ? ' dry run' : ''} ok ${p.ok} · chg ${p.changed}`
}
const codeHref = (f: string) => '/code/' + f.split('/').map(encodeURIComponent).join('/')
const facts = computed(() => {
  const f = detail.value?.facts as Record<string, any> | undefined
  if (!f) return []
  const rows: [string, string][] = []
  const os = [f.ansible_distribution, f.ansible_distribution_version].filter(Boolean).join(' ')
  if (os) rows.push(['os', os])
  if (f.ansible_kernel) rows.push(['kernel', f.ansible_kernel])
  if (f.ansible_processor_vcpus || f.ansible_memtotal_mb) rows.push(['cpu / mem', [f.ansible_processor_vcpus && `${f.ansible_processor_vcpus} vCPU`, f.ansible_memtotal_mb && `${(f.ansible_memtotal_mb / 1024).toFixed(1)} GiB`].filter(Boolean).join(' · ')])
  if (f.ansible_python_version) rows.push(['python', f.ansible_python_version])
  if (f.ansible_default_ipv4) rows.push(['address', f.ansible_default_ipv4])
  if (f.ansible_architecture) rows.push(['arch', f.ansible_architecture])
  return rows
})
const levelShort = (l: string) => l.replace('inventory ', 'inv ').replace('playbook ', 'pb ')
</script>

<template>
  <AppShell>
    <header class="ihead">
      <h1>Inventory</h1>
      <div v-if="scopeCounts.length" class="seg-group" role="group" aria-label="Environment">
        <button v-for="s in scopeCounts" :key="s.project + s.env" class="seg" :class="{ on: s.current }" type="button" :aria-pressed="s.current" @click="setScope(s.project, s.env)">
          {{ s.env }}<template v-if="(data?.scopes.filter((x) => x.env === s.env).length ?? 0) > 1"> · {{ s.project }}</template><template v-if="s.current && data?.hosts"> · {{ data.hosts.length }}</template>
        </button>
      </div>
      <span v-if="data?.source" class="mono small muted">source: {{ data.source }}</span>
      <div class="actions">
        <button class="btn sm" type="button" :disabled="!scope" @click="quick('ans.ping')">{{ checked.size ? `Ping ${checked.size}` : 'Ping all' }}</button>
        <button class="btn sm" type="button" :disabled="!scope" @click="quick('ans.facts')">Gather facts</button>
        <button class="btn sm" type="button" :disabled="!scope" @click="openAdhoc">Ad-hoc command…</button>
        <button class="btn sm primary" type="button" :disabled="!scope || !data?.playbooks?.length" @click="openPlaybook">Run playbook{{ checked.size ? ` on ${checked.size} host${checked.size === 1 ? '' : 's'}` : '' }}</button>
      </div>
    </header>
    <p v-if="notice" class="notice small" role="status">{{ notice }}</p>
    <p v-if="data?.error" class="pad err" role="alert">{{ data.error }}</p>
    <p v-else-if="!data" class="pad muted">Loading…</p>

    <div v-else class="cols">
      <section class="groups" aria-label="Groups">
        <span class="lbl pl">Groups</span>
        <button v-for="g in data.groups" :key="g.name" type="button" class="grp mono" :class="{ on: group === g.name }" :style="{ paddingLeft: 10 + Math.min(g.depth, 3) * 12 + 'px' }" @click="group = g.name">
          {{ g.name }}
          <span class="cnt" :class="{ warn: g.down }">{{ g.total }}<template v-if="g.down"> · {{ g.down }} down</template></span>
        </button>
        <span class="lbl pl top">Files</span>
        <RouterLink v-for="f in data.files" :key="f" class="grp mono file" :to="codeHref(f)">{{ f }}</RouterLink>
      </section>

      <div class="main">
        <section class="hosts" aria-label="Hosts">
          <div class="hr head">
            <span><input type="checkbox" :checked="allChecked" aria-label="Select all" @change="toggleAll(($event.target as HTMLInputElement).checked)" /></span>
            <span>host</span><span>address</span><span>os</span><span>reachable</span><span>last play</span>
          </div>
          <div v-for="h in hosts" :key="h.name" class="hr" :class="{ sel: h.name === selected }">
            <span><input type="checkbox" :checked="checked.has(h.name)" :aria-label="`Select ${h.name}`" @change="($event.target as HTMLInputElement).checked ? checked.add(h.name) : checked.delete(h.name)" /></span>
            <button type="button" class="hname mono" @click="selectHost(h.name)">{{ h.name }}</button>
            <span class="mono muted">{{ h.address }}</span>
            <span class="muted">{{ h.os || '—' }}</span>
            <span class="pill" :class="'st-' + reach(h).cls" :title="h.reachable?.msg || (h.reachable?.reachable ? 'ping response time (includes connection setup)' : '')">{{ reach(h).text }}</span>
            <RouterLink v-if="h.last_play" class="mono small play" :to="'/runs/' + h.last_play.run_id" :class="{ fail: h.last_play.failures || h.last_play.unreachable }">{{ play(h) }}</RouterLink>
            <span v-else class="muted small">—</span>
          </div>
          <p v-if="!hosts.length" class="pad muted">No hosts in this group.</p>
        </section>

        <section v-if="selected" class="detail" aria-label="Host detail">
          <div class="facts">
            <div class="row">
              <span class="mono hn">{{ selected }}</span>
              <span v-if="detail?.reachable" class="pill" :class="detail.reachable.reachable ? 'st-ok' : 'st-fail'">{{ detail.reachable.reachable ? `up · ${detail.reachable.latency_ms} ms` : 'unreachable' }}</span>
            </div>
            <span class="lbl">Facts<template v-if="detail?.facts_gathered_at"> · gathered {{ ago(detail.facts_gathered_at, now) === 'now' ? 'just now' : ago(detail.facts_gathered_at, now) + ' ago' }}</template></span>
            <div v-if="facts.length" class="fgrid mono">
              <template v-for="[k, v] in facts" :key="k"><span>{{ k }}</span><span class="fv">{{ v }}</span></template>
            </div>
            <p v-else class="small muted">No facts yet. <button class="link" type="button" @click="checked.clear(); checked.add(selected); quick('ans.facts')">Gather facts</button></p>
            <span v-if="detail?.groups?.length" class="small muted">groups: <span class="mono">{{ detail.groups.join(', ') }}</span></span>
          </div>
          <div class="vars">
            <span class="lbl">Variables on this host · winner first</span>
            <p v-if="detailError" class="err small">{{ detailError }}</p>
            <div class="scroll">
              <table v-if="detail" class="mono vt">
                <thead><tr><th>variable</th><th>value</th><th>from</th></tr></thead>
                <tbody v-for="v in detail.vars.rows" :key="v.name">
                  <tr class="first">
                    <td>{{ v.name }}</td>
                    <td :class="{ secret: v.secret }">{{ v.value }}</td>
                    <td>
                      <RouterLink v-if="v.winner && v.status !== 'unknown'" :to="codeHref(v.winner.file)" class="src" :title="v.winner.level">{{ v.winner.file }}</RouterLink>
                      <span v-if="v.status === 'unknown'" class="badge warn" title="groundwork's resolution disagrees with ansible-inventory, or the value comes from a source it can't read">can't determine source</span>
                      <span v-else-if="v.status === 'role-default'" class="badge" title="Only set in role defaults: applies in plays that use the role">role default</span>
                      <span v-if="v.winner && v.status !== 'unknown'" class="lvl">{{ levelShort(v.winner.level) }}</span>
                    </td>
                  </tr>
                  <tr v-for="o in v.overridden" :key="o.file + o.level" class="ov">
                    <td></td><td>{{ o.value }}</td><td>{{ o.file }} <span class="lvl">{{ levelShort(o.level) }}</span></td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p v-for="f in detail?.vars.encrypted_files ?? []" :key="f" class="small muted">🔒 <span class="mono">{{ f }}</span> is vault-encrypted; its variables aren't listed here.</p>
          </div>
        </section>
      </div>
    </div>

    <div v-if="adhoc.open" class="modal" role="dialog" aria-modal="true" aria-labelledby="adhoc-title" @keydown.esc="adhoc.open = false">
      <form class="card dlg" @submit.prevent="runAdhoc">
        <h2 id="adhoc-title">Ad-hoc command · {{ scope?.env }}</h2>
        <label class="field">Hosts (pattern)<input v-model="adhoc.pattern" class="inp" type="text" /></label>
        <label class="field">Module<input v-model="adhoc.module" class="inp" type="text" placeholder="ping" /></label>
        <label class="field">Arguments<input v-model="adhoc.args" class="inp" type="text" placeholder="e.g. uptime" /></label>
        <label v-if="adhocMutating" class="field warnbox"><span><b class="mono">{{ adhoc.module }}</b> can change hosts. Type <b class="mono">{{ scope?.env }}</b> to run it.</span>
          <input v-model="adhoc.confirm" class="inp" type="text" autocomplete="off" :aria-label="`Type ${scope?.env} to confirm`" />
        </label>
        <p v-if="adhoc.error" class="err small" role="alert">{{ adhoc.error }}</p>
        <div class="dact">
          <button class="btn" type="button" @click="adhoc.open = false">Cancel</button>
          <button class="btn primary" type="submit" :disabled="adhocMutating && adhoc.confirm !== scope?.env">Run</button>
        </div>
      </form>
    </div>

    <div v-if="pb.open" class="modal" role="dialog" aria-modal="true" aria-labelledby="pb-title" @keydown.esc="pb.open = false">
      <form class="card dlg" @submit.prevent="runPlaybook">
        <h2 id="pb-title">Run playbook · {{ scope?.env }}</h2>
        <label class="field">Playbook
          <select v-model="pb.playbook" class="inp"><option v-for="p in data?.playbooks ?? []" :key="p" :value="p">{{ p }}</option></select>
        </label>
        <span class="small muted">Hosts: <span class="mono">{{ limit || 'all in the playbook' }}</span></span>
        <label class="field">Tags (optional)<input v-model="pb.tags" class="inp" type="text" placeholder="e.g. nginx" /></label>
        <fieldset class="modes">
          <label class="opt" :class="{ on: pb.mode === 'check' }"><input v-model="pb.mode" type="radio" value="check" />Dry run only<span class="small muted">--check --diff, changes nothing</span></label>
          <label class="opt" :class="{ on: pb.mode === 'run' }"><input v-model="pb.mode" type="radio" value="run" />Dry run, then run after approval</label>
        </fieldset>
        <p v-if="pb.error" class="err small" role="alert">{{ pb.error }}</p>
        <div class="dact">
          <button class="btn" type="button" @click="pb.open = false">Cancel</button>
          <button class="btn primary" type="submit" :disabled="!pb.playbook">Start</button>
        </div>
      </form>
    </div>
  </AppShell>
</template>

<style scoped>
.ihead { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 14px 24px; border-bottom: 1px solid var(--line); }
h1 { margin: 0; font-size: 18px; font-weight: 600; }
h2 { margin: 0; font-size: 15px; font-weight: 600; }
.small { font-size: 13px; }
.pad { padding: 24px; margin: 0; }
.notice { margin: 0; padding: 8px 24px; color: var(--ok); border-bottom: 1px solid var(--line); }
.seg-group { display: flex; gap: 2px; padding: 2px; border: 1px solid var(--line-strong); border-radius: 7px; background: #111518; }
.seg { font-size: 14px; height: 30px; padding: 0 12px; border: 0; border-radius: 5px; background: transparent; color: var(--text-muted); cursor: pointer; }
.seg.on { background: var(--line-strong); color: var(--text); }
.actions { margin-left: auto; display: flex; flex-wrap: wrap; gap: 8px; }
.cols { display: flex; flex-wrap: wrap; flex: 1; min-height: 0; }
.groups { flex: 1 1 200px; max-width: 260px; padding: 16px 10px; border-right: 1px solid var(--line); display: flex; flex-direction: column; gap: 2px; background: #0F1315; overflow: auto; }
.pl { padding: 4px 10px 8px; } .top { padding-top: 18px; }
.grp { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 8px; padding: 7px 10px; border-radius: 5px; color: #C3CBD0; font-size: 13.5px; cursor: pointer; text-decoration: none; word-break: break-all; }
.grp:hover { background: #1A1F23; } .grp.on { background: #1C2226; color: var(--text); }
.grp:focus-visible { outline: 2px solid var(--accent); }
.grp.file { color: var(--text-muted); font-size: 13px; }
.cnt { margin-left: auto; color: var(--text-label); white-space: nowrap; }
.cnt.warn { color: var(--warn); }
.main { flex: 999 1 560px; min-width: 0; display: flex; flex-direction: column; overflow: auto; }
.hosts { overflow-x: auto; }
.hr { display: grid; grid-template-columns: 28px minmax(130px, 1.2fr) 120px minmax(120px, 1fr) 130px 150px; gap: 12px; align-items: center; padding: 9px 16px; border-top: 1px solid #1A1F23; font-size: 13.5px; min-width: 720px; }
.hr.head { border-top: 0; color: var(--text-label); font-size: 13px; }
.hr.sel { background: #161D12; box-shadow: inset 3px 0 0 var(--accent); }
.hname { all: unset; cursor: pointer; font-weight: 500; color: var(--text); }
.hname:focus-visible { outline: 2px solid var(--accent); }
.play { color: var(--text-muted); text-decoration: none; } .play.fail { color: var(--fail); }
.pill { font: 500 12px var(--font-mono); padding: 3px 8px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; justify-self: start; white-space: nowrap; }
.st-ok { color: var(--ok); background: rgba(95, 211, 141, .10); } .st-fail { color: var(--fail); background: rgba(255, 122, 122, .10); }
.st-idle { color: var(--text-muted); background: rgba(154, 164, 171, .10); }
.detail { margin: 16px 24px 28px; border: 1px solid var(--line); border-radius: 10px; background: var(--bg-panel); display: flex; flex-wrap: wrap; }
.facts { flex: 1 1 260px; padding: 18px 20px; display: flex; flex-direction: column; gap: 12px; border-right: 1px solid var(--line); }
.row { display: flex; align-items: center; gap: 10px; }
.hn { font-size: 16px; font-weight: 700; }
.fgrid { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 6px 14px; font-size: 13px; color: var(--text-muted); }
.fv { color: var(--text); overflow-wrap: anywhere; }
.vars { flex: 2 1 420px; min-width: 0; padding: 18px 20px; display: flex; flex-direction: column; gap: 12px; }
.scroll { overflow-x: auto; }
.vt { width: 100%; min-width: 520px; border-collapse: collapse; font-size: 13.5px; table-layout: fixed; }
.vt th:nth-child(1) { width: 30%; } .vt th:nth-child(2) { width: 32%; }
.vt td { overflow-wrap: anywhere; }
.vt th { font-weight: 500; padding: 6px 0; color: var(--text-label); text-align: left; }
.vt tr.first td { border-top: 1px solid var(--line); padding: 8px 8px 8px 0; vertical-align: top; }
.vt tr.ov td { padding: 2px 8px 8px 0; text-decoration: line-through; color: var(--text-faint); }
.vt tr.ov td:first-child { text-decoration: none; }
.secret { color: var(--warn); }
.src { color: var(--accent); text-decoration: none; }
.lvl { display: block; font-size: 12px; color: var(--text-label); text-decoration: none; }
.badge { font-size: 12px; padding: 2px 6px; border-radius: 4px; background: rgba(154, 164, 171, .10); color: var(--text-muted); }
.badge.warn { color: var(--warn); background: rgba(245, 182, 71, .10); }
.link { background: none; border: 0; color: var(--accent); cursor: pointer; padding: 0; font: inherit; }
.modal { position: fixed; inset: 0; background: rgba(0, 0, 0, .55); display: grid; place-items: center; z-index: 50; padding: 16px; }
.dlg { width: min(480px, 100%); }
.warnbox { padding: 10px; border: 1px solid #4A3B1A; border-radius: 8px; background: #1D1810; }
.modes { border: 0; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 8px; }
.modes .opt { flex-wrap: wrap; padding: 10px 14px; }
.dact { display: flex; justify-content: flex-end; gap: 8px; }
@media (max-width: 900px) { .groups { max-width: 100%; } }
</style>
