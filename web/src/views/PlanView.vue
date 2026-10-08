<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from '../components/AppShell.vue'
import StatusPill from '../components/StatusPill.vue'
import { api, ApiError } from '../api/client'
import type { PlanChange, PlanReview, ResourceDiff } from '../api/types'
import { ago, useRuns } from '../stores/runs'
import { onEvent } from '../composables/events'
import { useNow } from '../composables/now'

const route = useRoute()
const router = useRouter()
const runs = useRuns()
const now = useNow(10_000)

const id = computed(() => Number(route.params.id))
const review = ref<PlanReview | null>(null)
const loadError = ref('')

async function load() {
  try {
    review.value = await api<PlanReview>(`/runs/${id.value}/plan`)
    loadError.value = ''
    if (!selected.value && review.value.plan.changes.length) select(firstInterestingAddress())
  } catch (e) {
    loadError.value = e instanceof ApiError && e.status === 404 ? 'This run has no plan.' : String(e)
  }
}
onMounted(load)
watch(id, () => { selected.value = ''; diff.value = null; load() })
onEvent('run.updated', (d: { id: number }) => { if (d.id === id.value) load() })
onEvent('checks.updated', () => load())

// ---- resource list ----
type Filter = 'all' | 'replace' | 'update' | 'create' | 'delete'
const filter = ref<Filter>('all')
const changes = computed(() => review.value?.plan.changes ?? [])
const counts = computed(() => {
  const c: Record<string, number> = { all: changes.value.length, replace: 0, update: 0, create: 0, delete: 0 }
  for (const x of changes.value) c[x.action] = (c[x.action] ?? 0) + 1
  return c
})
const shown = computed(() => changes.value.filter((c) => filter.value === 'all' || c.action === filter.value))
const dangerSet = computed(() => new Set((review.value?.danger ?? []).map((d) => d.address)))
// open the most consequential change first: dangerous, then replacements, then deletes
function firstInterestingAddress(): string {
  const cs = changes.value
  return (cs.find((c) => dangerSet.value.has(c.address)) ?? cs.find((c) => c.action === 'replace') ?? cs.find((c) => c.action === 'delete') ?? cs[0]).address
}
const SYM: Record<string, string> = { create: '+', update: '~', replace: '±', delete: '−', read: '·' }
const LABEL: Record<string, string> = { create: 'new', update: 'change', replace: 'replace', delete: 'destroy', read: 'read' }
const VERB: Record<string, string> = {
  create: 'will be created', update: 'will be updated in place', replace: 'must be replaced', delete: 'will be destroyed', read: 'will be read',
}

// ---- diff (lazy, per resource) ----
const selected = ref('')
const diff = ref<ResourceDiff | null>(null)
const location = ref<{ file: string; line: number } | null>(null)
const showUnchanged = ref(false)
const diffError = ref('')
async function select(address: string) {
  selected.value = address
  showUnchanged.value = false
  diffError.value = ''
  try {
    const r = await api<{ diff: ResourceDiff; location?: { file: string; line: number } }>(
      `/runs/${id.value}/plan/resource?address=${encodeURIComponent(address)}`)
    if (selected.value !== address) return
    diff.value = r.diff
    location.value = r.location ?? null
  } catch (e) {
    diff.value = null
    diffError.value = e instanceof Error ? e.message : String(e)
  }
}
const codeLink = computed(() => location.value
  ? `/code/${location.value.file.split('/').map(encodeURIComponent).join('/')}?line=${location.value.line}` : '')
const why = computed<{ kind: string; attrs: string[] } | null>(() => {
  const d = diff.value
  if (!d || d.action !== 'replace') return null
  return { kind: d.reason ?? '', attrs: d.replace_paths ?? [] }
})

// ---- approval ----
const ack = ref(false)
const typed = ref('')
const busy = ref(false)
const approveError = ref('')
const staleReasons = ref<string[]>([])
const needsTyping = computed(() => !!review.value?.confirm_text)
const canApply = computed(() => {
  const r = review.value
  if (!r || !r.approvable || busy.value) return false
  if (r.danger.length && !ack.value) return false
  return !needsTyping.value || typed.value.trim() === r.confirm_text
})
const hint = computed(() => {
  const r = review.value
  if (!r) return ''
  if (r.stale.length) return 'This plan is stale. Re-plan to continue.'
  const missing = []
  if (r.danger.length && !ack.value) missing.push('tick the box')
  if (needsTyping.value && typed.value.trim() !== r.confirm_text) missing.push(`type ${r.confirm_text}`)
  return missing.length ? `To unlock: ${missing.join(' and ')}.` : `Ready. Logs will stream to run #${id.value}.`
})
async function apply() {
  busy.value = true
  approveError.value = ''
  staleReasons.value = []
  try {
    await runs.approve(id.value, typed.value.trim(), ack.value)
    router.push('/runs/' + id.value)
  } catch (e) {
    if (e instanceof ApiError && e.code === 'stale_plan') staleReasons.value = (e.details as { reasons: string[] }).reasons
    else approveError.value = e instanceof Error ? e.message : String(e)
    load()
  } finally { busy.value = false }
}
async function replan() {
  busy.value = true
  try {
    const r = await api<{ run: { id: number } }>(`/runs/${id.value}/replan`, { method: 'POST', body: {} })
    router.push(`/runs/${r.run.id}`)
  } catch (e) { approveError.value = e instanceof Error ? e.message : String(e) } finally { busy.value = false }
}
async function discard() {
  if (!window.confirm('Discard this plan?')) return
  await runs.cancel(id.value).catch(() => {})
  load()
}

const ageMinutes = computed(() => (review.value?.planned_at ? Math.round((now.value - new Date(review.value.planned_at).getTime()) / 60000) : 0))
const ageClass = computed(() => (review.value && ageMinutes.value * 60 > review.value.plan_ttl_seconds ? 'fail' : ageMinutes.value * 60 > (review.value?.plan_ttl_seconds ?? 3600) * 0.75 ? 'warn' : 'ok'))
const checkPill = (s: string) => ({ ok: ['ok', 'pass'], issues: ['warn', 'issues'], failed: ['fail', 'failed'], skipped: ['idle', 'skipped'], running: ['run', 'running'], idle: ['idle', 'not run'] }[s] ?? ['idle', s])
const allStale = computed(() => [...new Set([...(review.value?.stale ?? []), ...staleReasons.value])])
const ackText = computed(() => {
  const ds = review.value?.danger ?? []
  if (ds.length === 1) return `I understand ${ds[0].address} ${ds[0].action === 'replace' ? 'will be destroyed and recreated' : 'will be destroyed'}`
  return `I understand ${ds.length} data-bearing resources will be destroyed or recreated`
})
const symClass = (c: PlanChange | ResourceDiff) => 'a-' + c.action
</script>

<template>
  <AppShell>
    <p v-if="loadError" class="pad err" role="alert">{{ loadError }}</p>
    <p v-else-if="!review" class="pad muted">Loading…</p>
    <template v-else>
      <header class="phead">
        <div class="between">
          <div class="titles">
            <div class="row">
              <h1 class="mono">#{{ id }} plan · {{ review.target.env }}</h1>
              <StatusPill :status="review.status" />
            </div>
            <span class="muted sub">
              <span class="mono">{{ review.target.root }}</span>
              <template v-if="review.commit"> · commit <span class="mono">{{ review.commit.slice(0, 7) }}</span></template>
              <template v-if="review.state"> · state serial <span class="mono">{{ review.state.exists ? review.state.serial : 'none' }}</span></template>
              <template v-if="review.planned_at"> · planned {{ ago(review.planned_at, now) === 'now' ? 'just now' : ago(review.planned_at, now) + ' ago' }}</template>
              · saved to <span class="mono">{{ review.plan_file }}</span>
            </span>
          </div>
          <div class="actions">
            <button class="btn sm" type="button" :disabled="busy" @click="replan">Re-plan</button>
            <RouterLink class="btn sm" :to="'/runs/' + id">Raw output</RouterLink>
            <button v-if="review.status === 'waiting_approval'" class="btn sm" type="button" @click="discard">Discard</button>
          </div>
        </div>
        <div class="tiles">
          <div class="tile"><span class="mono n" style="color: var(--ok)">+{{ review.plan.create }}</span><span class="small muted">to add</span></div>
          <div class="tile"><span class="mono n" style="color: var(--running)">~{{ review.plan.update }}</span><span class="small muted">to change</span></div>
          <div class="tile" :class="{ hot: review.plan.replace }"><span class="mono n" :style="{ color: review.plan.replace ? 'var(--fail)' : 'var(--text-label)' }">±{{ review.plan.replace }}</span><span class="small" :class="review.plan.replace ? 'hotlbl' : 'muted'">to replace</span></div>
          <div class="tile" :class="{ hot: review.plan.delete }"><span class="mono n" :style="{ color: review.plan.delete ? 'var(--fail)' : 'var(--text-label)' }">−{{ review.plan.delete }}</span><span class="small" :class="review.plan.delete ? 'hotlbl' : 'muted'">to destroy</span></div>
        </div>
        <div v-for="d in review.danger" :key="d.address" class="danger" role="alert">
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="var(--fail)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3l9 16H3z" /><path d="M12 10v4M12 17h.01" /></svg>
          <span><b>{{ d.address }} {{ d.action === 'replace' ? 'will be destroyed and recreated.' : 'will be destroyed.' }}</b>{{ ' ' }}<span class="dsub">{{ d.message.slice(d.message.indexOf('. ') + 2) }}</span></span>
          <button class="btn sm" type="button" @click="select(d.address)">Show diff</button>
        </div>
        <div v-if="allStale.length && review.status === 'waiting_approval'" class="stale" role="alert">
          <b>Re-plan required.</b>
          <ul><li v-for="r in allStale" :key="r">{{ r }}</li></ul>
          <button class="btn sm" type="button" :disabled="busy" @click="replan">Re-plan now</button>
        </div>
      </header>

      <div class="cols">
        <section class="reslist" aria-label="Resources in plan">
          <div class="seg-group" role="group" aria-label="Filter by action">
            <template v-for="f in (['all', 'replace', 'update', 'create', 'delete'] as const)" :key="f">
              <button v-if="f === 'all' || counts[f]" class="seg" :class="{ on: filter === f }" type="button" :aria-pressed="filter === f" @click="filter = f">
                {{ { all: 'All', replace: 'Replace', update: 'Change', create: 'Add', delete: 'Destroy' }[f] }} {{ counts[f] }}
              </button>
            </template>
          </div>
          <button v-for="c in shown" :key="c.address" type="button" class="res" :class="{ on: c.address === selected, danger: dangerSet.has(c.address) }" @click="select(c.address)">
            <span class="sym mono" :class="symClass(c)">{{ SYM[c.action] }}</span>
            <span class="mono addr">{{ c.address }}</span>
            <span class="small" :class="dangerSet.has(c.address) || c.action === 'replace' || c.action === 'delete' ? 'hotlbl' : 'muted'">{{ LABEL[c.action] }}</span>
          </button>
          <p v-if="!changes.length" class="muted small pad0">No changes.</p>
        </section>

        <section class="diffpane" aria-label="Resource diff">
          <p v-if="diffError" class="pad err">{{ diffError }}</p>
          <template v-else-if="diff">
            <div class="dhdr">
              <span class="sym mono" :class="symClass(diff)">{{ SYM[diff.action] }}</span>
              <span class="mono daddr">{{ diff.address }}</span>
              <span class="small muted">{{ VERB[diff.action] }}</span>
              <RouterLink v-if="codeLink" class="small loc mono" :to="codeLink">{{ location!.file }}:{{ location!.line }}</RouterLink>
            </div>
            <div class="table">
              <div class="df head"><span></span><span>attribute</span><span>{{ diff.action === 'delete' ? 'before' : diff.action === 'create' ? 'value' : 'before → after' }}</span></div>
              <div v-for="a in diff.changed" :key="a.path" class="df" :class="{ forces: a.forces_replacement }">
                <span class="mono" :class="'k-' + a.kind">{{ a.forces_replacement ? '±' : { add: '+', remove: '−', change: '~', same: '' }[a.kind] }}</span>
                <span class="mono path">{{ a.path }}</span>
                <span class="mono val">
                  <template v-if="a.kind === 'add'"><span :class="{ dim: a.after?.startsWith('(') }">{{ a.after }}</span></template>
                  <template v-else-if="a.kind === 'remove'"><span class="strike">{{ a.before }}</span></template>
                  <template v-else><span :class="{ dim: a.before?.startsWith('(') || a.before === 'null' }">{{ a.before }}</span> → <b :class="{ dim: a.after?.startsWith('(') }">{{ a.after }}</b></template>
                  <span v-if="a.forces_replacement" class="forcenote"> # forces replacement</span>
                </span>
              </div>
              <div v-if="diff.unchanged_count" class="df muted">
                <span></span><span>{{ diff.unchanged_count }} unchanged attribute{{ diff.unchanged_count === 1 ? '' : 's' }}</span>
                <span><button class="btn xs" type="button" @click="showUnchanged = !showUnchanged">{{ showUnchanged ? 'Hide' : 'Show' }}</button></span>
              </div>
              <template v-if="showUnchanged">
                <div v-for="a in diff.unchanged" :key="'u' + a.path" class="df same"><span></span><span class="mono path">{{ a.path }}</span><span class="mono val">{{ a.before }}</span></div>
              </template>
              <p v-if="diff.truncated" class="small muted pad0">This resource is very large; only the first 1,000 attributes are shown.</p>
            </div>
            <div v-if="why" class="why">
              <span class="lbl">Why it's replaced</span>
              <span v-if="why.kind === 'replace_by_request'" class="whytext">A replacement was requested explicitly (<span class="mono">-replace</span>).</span>
              <span v-else-if="why.kind === 'replace_because_tainted'" class="whytext">The resource is tainted (a previous create failed part-way), so Terraform recreates it.</span>
              <span v-else class="whytext">
                Terraform can't change <template v-if="why.attrs.length"><template v-for="(a, i) in why.attrs" :key="a"><span v-if="i">, </span><span class="mono code">{{ a }}</span></template></template><template v-else>one of its arguments</template>
                in place, so it destroys this resource and creates a new one. To make sure this can never be applied by accident, add
                <span class="mono code">lifecycle { prevent_destroy = true }</span> to the resource.
              </span>
              <RouterLink v-if="codeLink" class="btn sm" :to="codeLink">Open in editor</RouterLink>
            </div>
          </template>
          <p v-else class="pad muted">Select a resource to see its changes.</p>
        </section>

        <aside class="approve" aria-label="Approve">
          <div class="checks">
            <span class="lbl">Checks on this plan</span>
            <div class="cgrid">
              <template v-for="c in review.checks" :key="c.tool">
                <span class="mono small">{{ c.tool }}</span><span class="pill" :class="'st-' + checkPill(c.status)[0]">{{ c.status === 'issues' ? c.count + ' issues' : checkPill(c.status)[1] }}</span>
              </template>
              <span class="mono small">plan age</span><span class="pill" :class="'st-' + ageClass">{{ ageMinutes }}m / {{ Math.round(review.plan_ttl_seconds / 60) }}m</span>
              <span class="mono small">config changed since</span><span class="pill" :class="review.stale.some((r) => r.includes('configuration')) ? 'st-fail' : 'st-ok'">{{ review.stale.some((r) => r.includes('configuration')) ? 'yes' : 'no' }}</span>
              <span class="mono small">state changed since</span><span class="pill st-idle">checked on apply</span>
              <span class="mono small">destroys data</span><span class="pill" :class="review.danger.length ? 'st-fail' : 'st-ok'">{{ review.danger.length ? 'yes' : 'no' }}</span>
            </div>
          </div>

          <div v-if="review.status === 'waiting_approval'" class="gate">
            <span class="gtitle">Apply to {{ review.target.env }}</span>
            <span class="small gtext">Applies exactly this saved plan. If the state or the configuration changes first, you'll have to re-plan.</span>
            <label v-if="review.danger.length" class="ackl"><input v-model="ack" type="checkbox" />{{ ackText }}</label>
            <label v-if="needsTyping" class="typel"><span>Type <span class="mono" style="color: var(--text)">{{ review.confirm_text }}</span> to confirm</span>
              <input v-model="typed" class="inp" type="text" autocomplete="off" spellcheck="false" :aria-label="`Type ${review.confirm_text} to confirm`" />
            </label>
            <p v-if="approveError" class="err" role="alert">{{ approveError }}</p>
            <button class="btn applybtn" :class="{ hotbtn: review.danger.length || review.plan.replace || review.plan.delete }" type="button" :disabled="!canApply" @click="apply">{{ busy ? 'Checking…' : `Apply plan #${id}` }}</button>
            <span class="small muted">{{ hint }}</span>
          </div>
          <div v-else class="gate done">
            <span class="gtitle">{{ { succeeded: review.apply ? 'Applied' : 'Finished', failed: 'Apply failed', cancelled: 'Discarded', running: 'Applying…', queued: 'Queued' }[review.status] ?? review.status }}</span>
            <span v-if="review.approval" class="small muted">Approved by {{ review.approval.approved_by }} · {{ new Date(review.approval.approved_at).toLocaleString() }}<template v-if="review.approval.acknowledged_danger"> · danger acknowledged</template></span>
            <span v-if="review.error" class="small err">{{ review.error }}</span>
            <RouterLink class="btn sm" :to="'/runs/' + id">Open run #{{ id }}</RouterLink>
          </div>
        </aside>
      </div>
    </template>
  </AppShell>
</template>

<style scoped>
.pad { padding: 24px; margin: 0; } .pad0 { padding: 6px; margin: 0; }
.small { font-size: 13px; }
.phead { padding: 20px 24px; border-bottom: 1px solid var(--line); display: flex; flex-direction: column; gap: 16px; }
.between { display: flex; flex-wrap: wrap; align-items: flex-start; justify-content: space-between; gap: 12px; }
.titles { display: flex; flex-direction: column; gap: 6px; }
.row { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
h1 { margin: 0; font-size: 20px; font-weight: 700; }
.sub { font-size: 14px; }
.actions { display: flex; flex-wrap: wrap; gap: 8px; }
.tiles { display: flex; flex-wrap: wrap; gap: 10px; }
.tile { display: flex; flex-direction: column; gap: 2px; padding: 12px 16px; border: 1px solid var(--line); border-radius: 8px; background: var(--bg-panel); min-width: 110px; }
.tile .n { font-size: 22px; }
.tile.hot { border-color: var(--fail-line); background: var(--fail-bg); }
.hotlbl { color: var(--fail-strong); }
.danger { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 14px; padding: 12px 16px; border: 1px solid var(--fail-line); border-radius: 8px; background: var(--fail-bg); font-size: 14.5px; }
.danger .btn { margin-left: auto; height: 32px; }
.dsub { color: var(--fail-strong); }
.stale { padding: 12px 16px; border: 1px solid var(--warn-line); border-radius: 8px; background: var(--warn-bg); color: var(--warn-strong); font-size: 14px; display: flex; flex-direction: column; gap: 6px; align-items: flex-start; }
.stale ul { margin: 0; padding-left: 18px; }
.cols { display: flex; flex-wrap: wrap; flex: 1; min-height: 0; }
.reslist { flex: 1 1 300px; max-width: 360px; padding: 16px 10px; border-right: 1px solid var(--line); display: flex; flex-direction: column; gap: 4px; background: var(--bg-sunken); overflow: auto; }
.seg-group { display: flex; flex-wrap: wrap; gap: 2px; padding: 2px; margin: 0 4px 8px; border: 1px solid var(--line-strong); border-radius: 7px; background: var(--bg-card); align-self: flex-start; }
.seg { font-size: 13px; height: 28px; padding: 0 10px; border: 0; border-radius: 5px; background: transparent; color: var(--text-muted); cursor: pointer; }
.seg.on { background: var(--line-strong); color: var(--text); }
.res { all: unset; box-sizing: border-box; display: grid; grid-template-columns: 24px minmax(0, 1fr) auto; gap: 10px; align-items: center; padding: 10px 12px; border-radius: 6px; cursor: pointer; }
.res:hover { background: var(--bg-raised); }
.res.on { background: var(--bg-active); box-shadow: inset 0 0 0 1px var(--line-strong); }
.res.on.danger { background: var(--fail-bg); box-shadow: inset 0 0 0 1px var(--fail-line); }
.res:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
.addr { font-size: 13.5px; overflow-wrap: anywhere; }
.sym { font-weight: 700; text-align: center; }
.a-create { color: var(--ok); } .a-update { color: var(--running); } .a-replace { color: var(--fail); } .a-delete { color: var(--fail); } .a-read { color: var(--text-label); }
.diffpane { flex: 999 1 440px; min-width: 0; display: flex; flex-direction: column; overflow: auto; }
.dhdr { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 14px 16px; border-bottom: 1px solid var(--line); }
.daddr { font-size: 14px; font-weight: 700; overflow-wrap: anywhere; }
.loc { margin-left: auto; }
.table { padding: 10px 0; overflow-x: auto; }
.df { display: grid; grid-template-columns: 22px minmax(140px, 260px) minmax(0, 1fr); gap: 12px; padding: 4px 16px; font: 13.5px/20px var(--font-mono); font-variant-ligatures: none; min-width: 520px; align-items: start; }
.df.head { color: var(--text-label); font-size: 12.5px; font-family: var(--font-ui); }
.df.forces { background: color-mix(in srgb, var(--fail) 7%, transparent); }
.df.same { color: var(--text-faint); }
.path { overflow-wrap: anywhere; }
.val { overflow-wrap: anywhere; white-space: pre-wrap; }
.k-add { color: var(--ok); } .k-remove { color: var(--fail); } .k-change { color: var(--running); }
.forces > span:first-child { color: var(--fail); }
.forcenote { color: var(--fail-strong); }
.dim { color: var(--text-label); font-weight: 400; }
.strike { text-decoration: line-through; color: var(--fail-strong); }
.btn.xs { height: 24px; font-size: 12px; padding: 0 8px; }
.why { margin: 6px 16px 16px; padding: 14px 16px; border: 1px solid var(--line); border-radius: 8px; background: var(--bg-panel); display: flex; flex-direction: column; gap: 8px; align-items: flex-start; }
.whytext { font-size: 14px; color: var(--text-code); line-height: 1.55; }
.code { font-size: 13px; padding: 1px 5px; border-radius: 4px; background: var(--bg-inset); border: 1px solid var(--line); }
.approve { flex: 1 1 300px; max-width: 380px; border-left: 1px solid var(--line); padding: 18px; display: flex; flex-direction: column; gap: 18px; background: var(--bg-sunken); }
.checks { display: flex; flex-direction: column; gap: 10px; }
.cgrid { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 10px 8px; align-items: center; }
.pill { font: 500 12px var(--font-mono); padding: 3px 8px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; justify-self: end; }
.st-ok { color: var(--ok); background: color-mix(in srgb, var(--ok) 10%, transparent); } .st-warn { color: var(--warn); background: color-mix(in srgb, var(--warn) 10%, transparent); }
.st-fail { color: var(--fail); background: color-mix(in srgb, var(--fail) 10%, transparent); } .st-idle { color: var(--text-muted); background: color-mix(in srgb, var(--text-muted) 10%, transparent); }
.st-run { color: var(--running); background: color-mix(in srgb, var(--running) 12%, transparent); }
.gate { display: flex; flex-direction: column; gap: 12px; padding: 16px; border: 1px solid var(--fail-line); border-radius: 10px; background: var(--fail-bg); }
.gate.done { border-color: var(--line); background: var(--bg-panel); }
.gtitle { font-weight: 600; }
.gtext { color: var(--text-soft); line-height: 1.5; }
.ackl { display: flex; align-items: flex-start; gap: 10px; font-size: 14px; line-height: 1.45; }
.ackl input { margin-top: 3px; }
.typel { display: flex; flex-direction: column; gap: 6px; font-size: 13px; color: var(--text-muted); }
.applybtn { height: 40px; justify-content: center; background: var(--accent); border-color: var(--accent); color: var(--on-accent); font-weight: 600; }
.applybtn.hotbtn { background: var(--fail); border-color: var(--fail); color: var(--fail-bg); }
.applybtn:disabled { opacity: .4; }
@media (max-width: 1100px) { .reslist, .approve { max-width: 100%; border: 0; } }
</style>
