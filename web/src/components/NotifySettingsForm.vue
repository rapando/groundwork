<script setup lang="ts">
// Notification settings: which events reach which channel. For every project
// they are saved by the service (notify.yaml in its data directory); for one
// project, what it changes is written to the notify: block of its
// groundwork.yaml and everything else follows every project's settings.
import { computed, onMounted, ref } from 'vue'
import { api, serviceApi } from '../api/client'
import type { Channel, NotifyOverrides, NotifySettings, NotifySettingsResponse, NotifyTestResponse } from '../api/types'
import { desktopPermission, requestDesktop } from '../composables/notifyDelivery'

const props = defineProps<{
  scope: 'all' | 'project'
  /** environments to offer as chips (the project's; empty for every project) */
  envNames?: string[]
}>()
const emit = defineEmits<{ loaded: [r: NotifySettingsResponse] }>()

type EventKey = 'failed' | 'approval' | 'success' | 'drift'
type Key = EventKey | 'quiet_under' | 'envs' | 'webhook'
const EVENTS: { key: EventKey; label: string; hint: string }[] = [
  { key: 'failed', label: 'Run failed', hint: 'plan, apply, playbook, ad-hoc' },
  { key: 'approval', label: 'Waiting for approval', hint: 'plan or dry run is ready' },
  { key: 'success', label: 'Run succeeded', hint: 'only runs longer than the threshold' },
  { key: 'drift', label: 'Drift found', hint: 'scheduled checks' },
]
const CHANNELS: { key: Channel; label: string }[] = [
  { key: 'console', label: 'Console toast' },
  { key: 'desktop', label: 'Desktop' },
  { key: 'webhook', label: 'Webhook' },
]
const QUIET = ['0s', '10s', '30s', '1m', '2m', '5m', '10m', '30m']
const quietLabel = (d: string) => (d === '0s' ? 'nothing (notify every success)' : d)

const project = computed(() => props.scope === 'project')
const call = <T,>(path: string, init?: Parameters<typeof api>[1]) => (project.value ? api<T>(path, init) : serviceApi<T>(path, init))

// every project: the full settings. One project: its overrides (null or ""
// where it follows), shown over `inherited`.
const form = ref<NotifySettings | null>(null)
const over = ref<NotifyOverrides | null>(null)
const inherited = ref<NotifySettings | null>(null)
const shared = ref(true)
const saved = ref('')
const configured = ref(false)
const fileError = ref('')
const error = ref('')
const status = ref('')
const saving = ref(false)
const testing = ref(false)
const testLine = ref<{ ok: boolean; text: string } | null>(null)
const newEnv = ref('')

const msg = (e: unknown) => (e instanceof Error ? e.message : String(e))
const copy = (s: NotifySettings): NotifySettings => ({
  ...s, failed: [...(s.failed ?? [])], approval: [...(s.approval ?? [])], success: [...(s.success ?? [])], drift: [...(s.drift ?? [])],
  envs: [...(s.envs ?? [])], webhook: s.webhook ?? '',
})
const snapshot = () => JSON.stringify(project.value ? over.value : form.value)
const dirty = computed(() => (project.value ? !!over.value : !!form.value) && snapshot() !== saved.value)

function take(r: NotifySettingsResponse) {
  configured.value = r.configured
  fileError.value = r.error ?? ''
  if (project.value) {
    const o = r.overrides
    const base = copy(r.base ?? r.settings)
    const eff = copy(r.settings)
    // a key the project doesn't set shows what applies: usually the settings
    // for every project, but the legacy drift.notify / drift.webhook win
    const keys: Key[] = ['failed', 'approval', 'success', 'drift', 'envs']
    const inh = { ...base }
    for (const k of keys) if (!o || o[k as keyof NotifyOverrides] == null) (inh as Record<string, unknown>)[k] = eff[k as keyof NotifySettings]
    if (!o?.webhook) inh.webhook = eff.webhook
    if (!o?.quiet_under) inh.quiet_under = eff.quiet_under
    inherited.value = inh
    shared.value = r.shared !== false
    over.value = {
      failed: o?.failed ? [...o.failed] : null, approval: o?.approval ? [...o.approval] : null, success: o?.success ? [...o.success] : null,
      drift: o?.drift ? [...o.drift] : null, quiet_under: o?.quiet_under ?? '', envs: o?.envs ? [...o.envs] : null, webhook: o?.webhook ?? '',
    }
  } else {
    form.value = copy(r.settings)
  }
  saved.value = snapshot()
  emit('loaded', r)
}

async function load() {
  try {
    take(await call<NotifySettingsResponse>('/notifications/settings'))
  } catch (e) {
    error.value = msg(e)
  }
}

// what a key shows: the full settings, or the project's override over what it inherits
function value<K extends keyof NotifySettings>(k: K): NotifySettings[K] {
  if (!project.value) return form.value![k]
  const o = over.value![k as keyof NotifyOverrides]
  return (o == null || o === '' ? inherited.value![k] : o) as NotifySettings[K]
}
const overridden = (k: Key) => {
  const o = over.value?.[k as keyof NotifyOverrides]
  return project.value && o != null && o !== ''
}
function set<K extends keyof NotifySettings>(k: K, v: NotifySettings[K]) {
  if (project.value) (over.value as Record<string, unknown>)[k] = v
  else (form.value as Record<string, unknown>)[k] = v
  status.value = ''
}
function reset(k: Key) {
  if (!over.value) return
  ;(over.value as Record<string, unknown>)[k] = k === 'quiet_under' || k === 'webhook' ? '' : null
  status.value = ''
}
const overrideCount = computed(() => (['failed', 'approval', 'success', 'drift', 'quiet_under', 'envs', 'webhook'] as Key[]).filter(overridden).length)
const sourceName = computed(() => (shared.value ? 'All projects' : 'Default'))

const has = (ev: EventKey, ch: Channel) => value(ev).includes(ch)
function toggle(ev: EventKey, ch: Channel) {
  const cur = value(ev)
  set(ev, has(ev, ch) ? cur.filter((c) => c !== ch) : CHANNELS.map((c) => c.key).filter((c) => c === ch || cur.includes(c)))
}
const envs = computed(() => [...new Set([...(props.envNames ?? []), ...value('envs')])])
function toggleEnv(e: string) {
  const cur = value('envs')
  set('envs', cur.includes(e) ? cur.filter((x) => x !== e) : [...cur, e])
}
function addEnv() {
  const e = newEnv.value.trim()
  if (e && !value('envs').includes(e)) set('envs', [...value('envs'), e])
  newEnv.value = ''
}
const quietOptions = computed(() => {
  const q = value('quiet_under')
  return QUIET.includes(q) ? QUIET : [q, ...QUIET]
})
const webhookText = computed({
  get: () => (project.value ? over.value!.webhook : form.value!.webhook),
  set: (v: string) => set('webhook', v),
})

async function save() {
  if (saving.value || !dirty.value) return
  saving.value = true
  error.value = ''
  status.value = ''
  try {
    const body = project.value ? { ...over.value!, webhook: over.value!.webhook.trim() } : { ...form.value!, webhook: form.value!.webhook.trim() }
    take(await call<NotifySettingsResponse>('/notifications/settings', { method: 'PUT', body }))
    status.value = project.value ? (overrideCount.value ? 'Saved to groundwork.yaml' : 'Following all projects') : 'Saved for every project'
  } catch (e) {
    error.value = msg(e)
  } finally {
    saving.value = false
  }
}

const NAMES: Record<Channel, string> = { console: 'the console', desktop: 'desktop', webhook: 'webhook' }
const list = (xs: string[]) => (xs.length < 2 ? xs.join('') : xs.slice(0, -1).join(', ') + ' and ' + xs[xs.length - 1])
async function test() {
  if (testing.value) return
  testing.value = true
  testLine.value = null
  try {
    // the click is the moment the browser lets us ask
    if (desktopPermission.value === 'default') await requestDesktop()
    const r = await call<NotifyTestResponse>('/notifications/test', { method: 'POST' })
    const parts: string[] = []
    if (r.sent.length) parts.push(`Test sent to ${list(r.sent.map((c) => NAMES[c]))}.`)
    for (const e of r.errors ?? []) parts.push(`${NAMES[e.channel][0].toUpperCase() + NAMES[e.channel].slice(1)} failed: ${e.error}`)
    if (!parts.length) parts.push('Nothing to test: turn on Desktop or Webhook for an event, or add a webhook URL, and save.')
    testLine.value = { ok: !r.errors?.length && r.sent.length > 0, text: parts.join(' ') }
  } catch (e) {
    testLine.value = { ok: false, text: msg(e) }
  } finally {
    testing.value = false
  }
}

onMounted(load)
defineExpose({ load })
</script>

<template>
  <div class="wrap">
    <p v-if="error" class="err" role="alert">{{ error }}</p>
    <p v-if="fileError" class="err" role="alert">{{ fileError }}. The built-in defaults apply until it is fixed or saved again.</p>

    <form v-if="project ? over && inherited : form" class="card settings" @submit.prevent="save">
      <table class="matrix">
        <caption class="sr">Which events reach which channel</caption>
        <thead>
          <tr>
            <th scope="col">Event</th><th v-for="c in CHANNELS" :key="c.key" scope="col">{{ c.label }}
              <template v-if="c.key === 'desktop'">
                <a v-if="desktopPermission === 'denied'" class="perm blocked" href="#browser-title">blocked here</a>
                <button v-else-if="desktopPermission === 'default'" class="perm" type="button" @click="requestDesktop()">allow</button>
              </template>
            </th>
            <th v-if="project" scope="col" class="from">From</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="ev in EVENTS" :key="ev.key" :class="{ inh: project && !overridden(ev.key) }">
            <th scope="row"><span class="evl">{{ ev.label }}</span><span class="evh">{{ ev.hint }}</span></th>
            <td v-for="c in CHANNELS" :key="c.key">
              <input type="checkbox" class="tog" :checked="has(ev.key, c.key)" :aria-label="`${ev.label}: ${c.label}`"
                :title="c.key === 'webhook' && !value('webhook').trim() ? 'Add a webhook URL below to use this' : undefined" @change="toggle(ev.key, c.key)">
            </td>
            <td v-if="project" class="from">
              <span v-if="overridden(ev.key)" class="src ov">This project
                <button class="link" type="button" :aria-label="`Reset ${ev.label} to ${sourceName.toLowerCase()}`" @click="reset(ev.key)">Reset</button></span>
              <span v-else class="src">{{ sourceName }}</span>
            </td>
          </tr>
        </tbody>
      </table>

      <div class="fields">
        <div class="field">
          <span class="fhead"><label class="flabel" for="quiet">Skip runs shorter than</label>
            <template v-if="project"><span v-if="overridden('quiet_under')" class="src ov">this project <button class="link" type="button" :aria-label="`Reset the threshold to ${sourceName.toLowerCase()}`" @click="reset('quiet_under')">Reset</button></span><span v-else class="src">{{ sourceName.toLowerCase() }}</span></template>
          </span>
          <select id="quiet" class="inp sel" :class="{ inh: project && !overridden('quiet_under') }" :value="value('quiet_under')" aria-describedby="quiet-hint"
            @change="set('quiet_under', ($event.target as HTMLSelectElement).value)">
            <option v-for="q in quietOptions" :key="q" :value="q">{{ quietLabel(q) }}</option>
          </select>
          <span id="quiet-hint" class="hint">Successes quicker than this go to the list only. Failures always notify.</span>
        </div>

        <fieldset class="field envs">
          <legend class="flabel">Environments
            <template v-if="project"><span v-if="overridden('envs')" class="src ov">this project <button class="link" type="button" :aria-label="`Reset the environment filter to ${sourceName.toLowerCase()}`" @click="reset('envs')">Reset</button></span><span v-else class="src">{{ sourceName.toLowerCase() }}</span></template>
          </legend>
          <div class="chips" :class="{ inh: project && !overridden('envs') }">
            <label v-for="e in envs" :key="e" class="chip" :class="{ on: value('envs').includes(e) }">
              <input type="checkbox" :checked="value('envs').includes(e)" @change="toggleEnv(e)"><span class="mono">{{ e }}</span>
            </label>
            <input v-if="!project" v-model="newEnv" class="inp addenv mono" placeholder="add, e.g. prod" aria-label="Add an environment" autocomplete="off" spellcheck="false" @keydown.enter.prevent="addEnv" @blur="addEnv">
            <span v-else-if="!envs.length" class="hint">No environments configured.</span>
          </div>
          <span class="hint">{{ value('envs').length ? `Desktop and webhook only for ${list(value('envs'))}.` : 'All environments. Tick some to limit desktop and webhook to them.' }}</span>
        </fieldset>

        <div class="field wide">
          <span class="fhead"><label class="flabel" for="hook">Webhook URL</label>
            <template v-if="project"><span v-if="overridden('webhook')" class="src ov">this project <button class="link" type="button" :aria-label="`Use the webhook from ${sourceName.toLowerCase()}`" @click="reset('webhook')">Reset</button></span><span v-else class="src">{{ sourceName.toLowerCase() }}</span></template>
          </span>
          <input id="hook" v-model="webhookText" class="inp" :class="{ inh: project && !overridden('webhook') }" type="text" inputmode="url" autocomplete="off" spellcheck="false" aria-describedby="hook-hint"
            :placeholder="project && inherited?.webhook ? inherited.webhook : 'https://hooks.example.com/services/…'">
          <span id="hook-hint" class="hint">Slack-compatible: a JSON body with <span class="mono">text</span> plus the event, project, environment and run.<template v-if="project && shared"> Keep a personal hook in All projects so it stays out of the repository.</template></span>
        </div>
      </div>

      <div class="row">
        <button class="btn primary" type="submit" :disabled="!dirty || saving">{{ saving ? 'Saving…' : project ? 'Save overrides' : 'Save' }}</button>
        <button class="btn" type="button" :disabled="testing" :aria-describedby="dirty ? 'test-hint' : undefined" @click="test">{{ testing ? 'Sending…' : 'Send a test' }}</button>
        <span v-if="dirty" id="test-hint" class="hint">Send a test uses the saved settings.</span>
        <span v-else-if="project" class="hint">{{ overrideCount ? `${overrideCount} override${overrideCount === 1 ? '' : 's'} in groundwork.yaml` : `Nothing overridden: this project follows ${sourceName.toLowerCase()}.` }}</span>
        <span class="result" role="status">
          <span v-if="status" class="ok">{{ status }}</span>
          <span v-else-if="testLine" :class="testLine.ok ? 'ok' : 'fail'">{{ testLine.text }}</span>
        </span>
      </div>
    </form>
    <p v-else-if="!error" class="muted">Loading…</p>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 14px; }
.matrix { width: 100%; border-collapse: collapse; font-size: 14px; }
.matrix th, .matrix td { padding: 10px 12px; border-bottom: 1px solid var(--line); text-align: center; }
.matrix thead th { font-size: 12.5px; font-weight: 600; color: var(--text-label); text-transform: uppercase; letter-spacing: .07em; }
.perm { all: unset; display: block; margin-top: 2px; font: 400 12.5px var(--font-ui); text-transform: none; letter-spacing: 0; color: var(--accent); cursor: pointer; text-decoration: underline; }
.perm.blocked { color: var(--warn); }
.perm:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.matrix th:first-child, .matrix .from { text-align: left; }
.matrix tbody tr:last-child > * { border-bottom: 0; }
.matrix tbody th { font-weight: 400; display: flex; flex-direction: column; gap: 2px; }
.evl { color: var(--text); font-weight: 500; }
.evh { font-size: 13px; color: var(--text-muted); }
.tog { width: 18px; height: 18px; cursor: pointer; }
tr.inh .tog, .inp.inh, .chips.inh .chip { opacity: .6; }
tr.inh .tog:hover, tr.inh .tog:focus-visible, .chips.inh .chip:hover, .inp.inh:focus { opacity: 1; }
.inp.inh { border-style: dashed; }
.src { font-size: 13px; color: var(--text-muted); font-weight: 400; white-space: nowrap; }
.envs legend .src { margin-left: 8px; }
.fhead { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.src.ov { color: var(--accent); font-weight: 600; }
.link { border: 0; background: none; padding: 0 0 0 6px; color: var(--text-muted); font: inherit; font-weight: 400; text-decoration: underline; text-underline-offset: 2px; cursor: pointer; }
.link:hover { color: var(--text); }
.fields { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 18px 24px; }
.field.wide { grid-column: 1 / -1; }
.flabel { font-size: 14px; font-weight: 600; color: var(--text); padding: 0; }
.sel { max-width: 320px; }
.envs { border: 0; margin: 0; padding: 0; min-width: 0; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.chip { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 10px; border: 1px solid var(--line-strong); border-radius: var(--r-control); background: var(--bg-panel); cursor: pointer; font-size: 13px; }
.chip.on { border-color: var(--accent); background: var(--accent-bg); }
.addenv { width: 140px; height: 32px; font-size: 13px; }
.hint { font-size: 13px; color: var(--text-muted); line-height: 1.5; }
.hint .mono { color: var(--text-code); }
.row { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.result { font-size: 14px; }
.sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
@media (max-width: 640px) { .matrix th, .matrix td { padding: 8px 6px; } }
</style>
