<script setup lang="ts">
// Notification settings for this project: which events reach which channel,
// written to the notify: block of groundwork.yaml.
import { computed, onMounted, ref } from 'vue'
import AppShell from '../components/AppShell.vue'
import { api } from '../api/client'
import type { Channel, NotifySettings, NotifySettingsResponse, NotifyTestResponse } from '../api/types'
import { useWorkspace } from '../stores/workspace'
import { desktopPermission, desktopSupported, requestDesktop } from '../composables/notifyDelivery'

type EventKey = 'failed' | 'approval' | 'success' | 'drift'
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

const ws = useWorkspace()
const form = ref<NotifySettings | null>(null)
const saved = ref('')
const configured = ref(false)
const error = ref('')
const status = ref('')
const saving = ref(false)
const testing = ref(false)
const testLine = ref<{ ok: boolean; text: string } | null>(null)

const msg = (e: unknown) => (e instanceof Error ? e.message : String(e))
const dirty = computed(() => !!form.value && JSON.stringify(form.value) !== saved.value)
const quietOptions = computed(() => (form.value && !QUIET.includes(form.value.quiet_under) ? [form.value.quiet_under, ...QUIET] : QUIET))

const RANK: Record<string, number> = { dev: 0, test: 1, qa: 1, staging: 2, prod: 3 }
const envs = computed(() => {
  const cfg = ws.ws?.config
  const set = new Set<string>(form.value?.envs ?? [])
  for (const r of cfg?.terraform?.roots ?? []) {
    if (r.env) set.add(r.env)
    for (const k of Object.keys(r.envs ?? {})) set.add(k)
  }
  for (const p of cfg?.ansible?.projects ?? []) for (const k of Object.keys(p.inventories ?? {})) set.add(k)
  return [...set].sort((a, b) => (RANK[a] ?? 9) - (RANK[b] ?? 9) || a.localeCompare(b))
})

function take(r: NotifySettingsResponse) {
  const s = r.settings
  form.value = { ...s, failed: [...(s.failed ?? [])], approval: [...(s.approval ?? [])], success: [...(s.success ?? [])], drift: [...(s.drift ?? [])], envs: [...(s.envs ?? [])], webhook: s.webhook ?? '' }
  saved.value = JSON.stringify(form.value)
  configured.value = r.configured
}

async function load() {
  try {
    take(await api<NotifySettingsResponse>('/notifications/settings'))
  } catch (e) {
    error.value = msg(e)
  }
}

const has = (ev: EventKey, ch: Channel) => !!form.value?.[ev].includes(ch)
function toggle(ev: EventKey, ch: Channel) {
  const f = form.value
  if (!f) return
  f[ev] = has(ev, ch) ? f[ev].filter((c) => c !== ch) : CHANNELS.map((c) => c.key).filter((c) => c === ch || f[ev].includes(c))
  status.value = ''
}
function toggleEnv(e: string) {
  const f = form.value
  if (!f) return
  f.envs = f.envs.includes(e) ? f.envs.filter((x) => x !== e) : [...f.envs, e]
  status.value = ''
}

async function save() {
  if (!form.value || saving.value) return
  saving.value = true
  error.value = ''
  status.value = ''
  try {
    take(await api<NotifySettingsResponse>('/notifications/settings', { method: 'PUT', body: { ...form.value, webhook: form.value.webhook.trim() } }))
    status.value = 'Saved to groundwork.yaml'
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
    const r = await api<NotifyTestResponse>('/notifications/test', { method: 'POST' })
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

onMounted(() => {
  document.title = 'Notifications · groundwork'
  if (desktopSupported()) desktopPermission.value = Notification.permission
  load()
})
</script>

<template>
  <AppShell>
    <div class="page">
      <header class="phead">
        <h1>Notifications</h1>
        <p class="muted">Saved to <span class="mono">groundwork.yaml</span> under <span class="mono">notify</span>. The console always lists every event.</p>
        <p v-if="form && !configured" class="muted small">These are the defaults. Saving writes them into the file so you can change them.</p>
      </header>

      <p v-if="error" class="err" role="alert">{{ error }}</p>

      <form v-if="form" class="card settings" @submit.prevent="save">
        <table class="matrix">
          <caption class="sr">Which events reach which channel</caption>
          <thead>
            <tr><th scope="col">Event</th><th v-for="c in CHANNELS" :key="c.key" scope="col">{{ c.label }}</th></tr>
          </thead>
          <tbody>
            <tr v-for="ev in EVENTS" :key="ev.key">
              <th scope="row"><span class="evl">{{ ev.label }}</span><span class="evh">{{ ev.hint }}</span></th>
              <td v-for="c in CHANNELS" :key="c.key">
                <input type="checkbox" class="tog" :checked="has(ev.key, c.key)" :aria-label="`${ev.label}: ${c.label}`"
                  :title="c.key === 'webhook' && !form.webhook.trim() ? 'Add a webhook URL below to use this' : undefined" @change="toggle(ev.key, c.key)">
              </td>
            </tr>
          </tbody>
        </table>

        <div class="fields">
          <label class="field">
            <span class="flabel">Skip runs shorter than</span>
            <select v-model="form.quiet_under" class="inp sel" aria-describedby="quiet-hint" @change="status = ''">
              <option v-for="q in quietOptions" :key="q" :value="q">{{ quietLabel(q) }}</option>
            </select>
            <span id="quiet-hint" class="hint">Successes quicker than this go to the list only. Failures always notify.</span>
          </label>

          <fieldset class="field envs">
            <legend class="flabel">Environments</legend>
            <div class="chips">
              <label v-for="e in envs" :key="e" class="chip" :class="{ on: form.envs.includes(e) }">
                <input type="checkbox" :checked="form.envs.includes(e)" @change="toggleEnv(e)"><span class="mono">{{ e }}</span>
              </label>
              <span v-if="!envs.length" class="hint">No environments configured.</span>
            </div>
            <span class="hint">{{ form.envs.length ? `Desktop and webhook only for ${list(form.envs)}.` : 'All environments. Tick some to limit desktop and webhook to them.' }}</span>
          </fieldset>

          <label class="field wide">
            <span class="flabel">Webhook URL</span>
            <input v-model="form.webhook" class="inp" type="text" inputmode="url" placeholder="https://hooks.example.com/services/…" autocomplete="off" spellcheck="false" aria-describedby="hook-hint" @input="status = ''">
            <span id="hook-hint" class="hint">Slack-compatible: a JSON body with <span class="mono">text</span> plus the event, project, environment and run.</span>
          </label>
        </div>

        <div class="row">
          <button class="btn primary" type="submit" :disabled="!dirty || saving">{{ saving ? 'Saving…' : 'Save' }}</button>
          <button class="btn" type="button" :disabled="testing" :aria-describedby="dirty ? 'test-hint' : undefined" @click="test">{{ testing ? 'Sending…' : 'Send a test' }}</button>
          <span v-if="dirty" id="test-hint" class="hint">Send a test uses the saved settings.</span>
          <span class="result" role="status">
            <span v-if="status" class="ok">{{ status }}</span>
            <span v-else-if="testLine" :class="testLine.ok ? 'ok' : 'fail'">{{ testLine.text }}</span>
          </span>
        </div>
      </form>
      <p v-else-if="!error" class="muted">Loading…</p>

      <section class="card browser" aria-labelledby="browser-title">
        <h2 id="browser-title">This browser</h2>
        <template v-if="desktopPermission === 'granted'">
          <p class="muted"><span class="ok">✓</span> Shows desktop notifications while a console tab is open. A click opens the run.</p>
        </template>
        <template v-else-if="desktopPermission === 'default'">
          <p class="muted">Allow notifications so a click on one opens the run here. Until then the service sends them itself.</p>
          <div><button class="btn" type="button" @click="requestDesktop()">Allow desktop notifications in this browser</button></div>
        </template>
        <p v-else-if="desktopPermission === 'denied'" class="muted">This browser blocks notifications from groundwork. Allow them in the site settings next to the address bar, then reload the page.</p>
        <p v-else class="muted">This browser can't show desktop notifications. The service sends them itself when no console is open.</p>
      </section>
    </div>
  </AppShell>
</template>

<style scoped>
.page { padding: 24px 32px; display: flex; flex-direction: column; gap: 20px; overflow: auto; max-width: 960px; }
.phead { display: flex; flex-direction: column; gap: 6px; }
h1 { margin: 0; font-size: 22px; font-weight: 600; }
h2 { margin: 0; font-size: 16px; font-weight: 600; }
.phead p, .browser p { margin: 0; font-size: 14.5px; }
.small { font-size: 13.5px; }
.matrix { width: 100%; border-collapse: collapse; font-size: 14px; }
.matrix th, .matrix td { padding: 10px 12px; border-bottom: 1px solid var(--line); text-align: center; }
.matrix thead th { font-size: 12px; font-weight: 500; color: var(--text-label); text-transform: uppercase; letter-spacing: .07em; }
.matrix th:first-child { text-align: left; }
.matrix tbody tr:last-child > * { border-bottom: 0; }
.matrix tbody th { font-weight: 400; display: flex; flex-direction: column; gap: 2px; }
.evl { color: var(--text); font-weight: 500; }
.evh { font-size: 13px; color: var(--text-muted); }
.tog { width: 18px; height: 18px; cursor: pointer; }
.fields { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 18px 24px; }
.field.wide { grid-column: 1 / -1; }
.flabel { font-size: 14px; font-weight: 600; color: var(--text); padding: 0; }
.sel { max-width: 320px; }
.envs { border: 0; margin: 0; padding: 0; min-width: 0; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.chip { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 10px; border: 1px solid var(--line-strong); border-radius: var(--r-control); background: var(--bg-panel); cursor: pointer; font-size: 13px; }
.chip.on { border-color: var(--accent); background: var(--accent-bg); }
.hint { font-size: 13px; color: var(--text-muted); line-height: 1.5; }
.hint .mono { color: var(--text-code); }
.row { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.result { font-size: 14px; }
.browser { gap: 10px; }
.sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
@media (max-width: 640px) { .page { padding: 20px 16px; } .matrix th, .matrix td { padding: 8px 6px; } }
</style>
