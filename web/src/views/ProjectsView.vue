<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { serviceApi } from '../api/client'
import type { ProjectView, ServiceInfo } from '../api/types'
import BrandMark from '../components/BrandMark.vue'
import ThemeToggle from '../components/ThemeToggle.vue'
import { swallow } from '../api/swallow'
import Modal from '../components/Modal.vue'
import SignInSettings from '../components/SignInSettings.vue'
import NotificationBell from '../components/NotificationBell.vue'
import { startNotifications, useNotifications } from '../stores/notifications'
import DropMenu from '../components/DropMenu.vue'
import StatusPill from '../components/StatusPill.vue'
import { shortPath } from '../composables/shortPath'
import { ago, kindLabel } from '../stores/runs'
import { useNow } from '../composables/now'

const projects = ref<ProjectView[] | null>(null)
const info = ref<ServiceInfo | null>(null)
const source = ref('')
const busy = ref(false)
const error = ref('')
const removing = ref<ProjectView | null>(null)
const removeError = ref('')
const signin = ref(false)
const notes = useNotifications()
startNotifications()
// reading notifications in the bell resolves the "1 failed" pills
watch(notes.serviceUnread, () => refresh())

// https://, ssh:// and git@host:path are cloned; anything else is a folder
const isURL = computed(() => /^(https:\/\/|ssh:\/\/|[\w.-]+@[\w.-]+:)/.test(source.value.trim()))

async function refresh() {
  try {
    projects.value = await serviceApi<ProjectView[]>('/projects')
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function importProject() {
  const s = source.value.trim()
  if (!s || busy.value) return
  busy.value = true
  error.value = ''
  try {
    const p = await serviceApi<{ id: string }>('/projects', { body: isURL.value ? { url: s } : { path: s } })
    location.assign(`/p/${p.id}/`)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    busy.value = false
  }
}

async function confirmRemove() {
  const p = removing.value
  if (!p) return
  removeError.value = ''
  try {
    await serviceApi(`/projects/${p.id}`, { method: 'DELETE' })
    removing.value = null
    await refresh()
  } catch (e) {
    removeError.value = e instanceof Error ? e.message : String(e)
  }
}

async function reopen(p: ProjectView) {
  try {
    await serviceApi(`/projects/${p.id}/reopen`, { method: 'POST' })
  } catch { /* the card keeps showing why */ }
  await refresh()
}

function status(p: ProjectView): { cls: string; text: string } {
  if (p.status !== 'ok') return { cls: 'fail', text: 'unavailable' }
  // what needs you comes first, in place of "ready", until you've read it
  if (p.unread_failed) return { cls: 'fail', text: `${p.unread_failed} failed` }
  if (p.waiting_approval) return { cls: 'warn', text: `${p.waiting_approval} waiting` }
  if (p.active_runs) return { cls: 'run', text: `${p.active_runs} running` }
  if (p.config_error) return { cls: 'fail', text: 'config error' }
  if (!p.configured) return { cls: 'warn', text: 'needs setup' }
  if (p.open_issues) return { cls: 'warn', text: `${p.open_issues} ${p.open_issues === 1 ? 'issue' : 'issues'}` }
  return { cls: 'ok', text: 'ready' }
}

const now = useNow(30_000)
const ENV: Record<string, string> = { failed: 'fail', pending: 'warn', drift: 'warn', running: 'run', ok: 'ok' }
const ENV_TEXT: Record<string, string> = { failed: 'failed', pending: 'approve', drift: 'drift', running: 'running' }
const sum = (k: 'unread_failed' | 'waiting_approval' | 'active_runs') => (projects.value ?? []).reduce((n, p) => n + (p[k] ?? 0), 0)
const headline = computed(() => {
  const ps = projects.value ?? []
  const parts: { text: string; cls: string }[] = [{ text: `${ps.length} ${ps.length === 1 ? 'project' : 'projects'}`, cls: '' }]
  const f = sum('unread_failed'), w = sum('waiting_approval'), r = sum('active_runs')
  if (f) parts.push({ text: `${f} failed ${f === 1 ? 'run' : 'runs'}`, cls: 'fail' })
  if (w) parts.push({ text: `${w} ${w === 1 ? 'plan' : 'plans'} waiting for approval`, cls: 'warn' })
  if (r) parts.push({ text: `${r} running`, cls: 'run' })
  return parts
})
function lastRun(p: ProjectView): string {
  const r = p.last_run
  if (!r) return ''
  return `#${r.id} ${kindLabel(r)}${r.target?.env ? ' · ' + r.target.env : ''}`
}
const copied = ref(false)
async function copyVersion() {
  if (!info.value) return
  try { await navigator.clipboard.writeText(`groundwork ${info.value.version}${info.value.commit ? ' (' + info.value.commit + ')' : ''}`); copied.value = true; setTimeout(() => (copied.value = false), 1500) } catch { /* the version stays readable on the page */ }
}

let timer = 0
const onFocus = () => refresh()
onMounted(async () => {
  document.title = 'Projects · groundwork'
  serviceApi<ServiceInfo>('/service').then((i) => (info.value = i)).catch(swallow('service info'))
  await refresh()
  timer = window.setInterval(refresh, 10_000)
  window.addEventListener('focus', onFocus)
})
onUnmounted(() => {
  clearInterval(timer)
  window.removeEventListener('focus', onFocus)
})
</script>

<template>
  <div class="shell">
    <header class="top">
      <BrandMark />
      <span class="mono name">groundwork</span>
      <span class="grow" />
      <NotificationBell align="right" />
      <button class="btn sm" type="button" @click="signin = true">Sign-in</button>
      <ThemeToggle />
    </header>

    <main class="body">
      <div class="head">
        <h1>Projects</h1>
        <p v-if="projects?.length" class="muted"><template v-for="(h, k) in headline" :key="h.text"><template v-if="k"> · </template><span :class="h.cls">{{ h.text }}</span></template></p>
        <p v-else class="muted">Each project is a repository groundwork checks, plans and runs. They all run in this one service.</p>
      </div>

      <form class="card import" @submit.prevent="importProject">
        <div class="row">
          <label class="flabel" for="import-src">Import</label>
          <input id="import-src" v-model="source" class="inp" :disabled="busy" placeholder="~/code/infra   or   git@github.com:acme/infra.git" aria-describedby="import-hint" autocomplete="off" spellcheck="false">
          <button class="btn" :class="{ primary: source.trim() }" type="submit" :disabled="busy || !source.trim()">
            {{ busy ? (isURL ? 'Cloning…' : 'Importing…') : isURL ? 'Clone and import' : 'Import folder' }}
          </button>
        </div>
        <p id="import-hint" class="hint">
          A folder is used in place (inside a git repository, its root is imported). A git URL is cloned with your own git and SSH keys<template v-if="info"> into <span class="mono" :title="info.repos_dir">{{ shortPath(info.repos_dir) }}</span></template>.
          From a terminal, <span class="mono">groundwork</span> in a repository does the same.
        </p>
        <p v-if="error" class="err" role="alert">{{ error }}</p>
      </form>

      <p v-if="projects && !projects.length" class="empty muted">No projects yet. Import one above to get started.</p>

      <div v-if="projects?.length" class="card table">
        <div class="thead" aria-hidden="true"><span>Project</span><span>Environments</span><span>Last run</span><span /></div>
        <ul class="list" aria-label="Projects">
          <li v-for="p in projects" :key="p.id" class="proj">
            <div class="pmain">
              <div class="title">
                <a v-if="p.status === 'ok'" class="pname" :href="`/p/${p.id}/`">{{ p.name }}</a>
                <span v-else class="pname">{{ p.name }}</span>
                <span v-if="status(p).text !== 'ready'" class="mono pill" :class="status(p).cls">{{ status(p).text }}</span>
              </div>
              <span class="mono path" :title="p.path">{{ shortPath(p.path) }}</span>
              <span v-if="p.remote" class="mono path" :title="p.remote">cloned from {{ p.remote }}</span>
              <span v-if="p.error" class="err">{{ p.error }}</span>
            </div>
            <div class="envs">
              <span v-for="e in p.envs ?? []" :key="e.name" class="env mono"><span class="dot" :class="ENV[e.status]" />{{ e.name }}<span v-if="ENV_TEXT[e.status]" :class="ENV[e.status]">{{ ENV_TEXT[e.status] }}</span></span>
              <span v-if="p.status === 'ok' && !(p.envs ?? []).length" class="muted small">none yet</span>
            </div>
            <div class="last">
              <template v-if="p.last_run">
                <span class="row1"><span class="mono">{{ lastRun(p) }}</span><StatusPill :status="p.last_run.status" /></span>
                <span class="muted small">{{ ago(p.last_run.created_at, now) === 'now' ? 'just now' : ago(p.last_run.created_at, now) + ' ago' }}</span>
              </template>
              <span v-else-if="p.status === 'ok'" class="muted small">no runs yet</span>
            </div>
            <div class="actions">
              <a v-if="p.status === 'ok'" class="btn sm primary" :href="`/p/${p.id}/`">Open</a>
              <button v-else class="btn sm" type="button" @click="reopen(p)">Retry</button>
              <DropMenu :label="`More actions for ${p.name}`" align="end">
                <template #button><span aria-hidden="true">⋯</span></template>
                <button class="mi dg" type="button" role="menuitem" :aria-label="`Remove ${p.name}`" @click="removing = p; removeError = ''">Remove…</button>
              </DropMenu>
            </div>
          </li>
        </ul>
      </div>
      <p v-if="info" class="ver mono">groundwork {{ info.version }} · data in <span :title="info.home">{{ shortPath(info.home) }}</span> · <button class="linkish" type="button" @click="copyVersion">{{ copied ? 'copied' : 'copy version' }}</button></p>
    </main>

    <Modal v-if="removing" labelledby="rm-title" @close="removing = null">
      <div class="card dialog">
        <h2 id="rm-title">Remove {{ removing.name }}?</h2>
        <p class="muted">groundwork stops watching it. Files in <span class="mono">{{ removing.path }}</span>, including its <span class="mono">.groundwork/</span> history, are left in place, so importing it again picks up where you left off.</p>
        <p v-if="removeError" class="err" role="alert">{{ removeError }}</p>
        <div class="row end">
          <button class="btn" type="button" @click="removing = null">Cancel</button>
          <button class="btn danger" type="button" @click="confirmRemove">Remove project</button>
        </div>
      </div>
    </Modal>

    <Modal v-if="signin" labelledby="signin-title" @close="signin = false">
      <SignInSettings>
        <div class="row end"><button class="btn" type="button" @click="signin = false">Close</button></div>
      </SignInSettings>
    </Modal>
  </div>
</template>

<style scoped>
.shell { min-height: 100vh; }
.top { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 16px 32px; border-bottom: 1px solid var(--line); }
.name { font-weight: 700; }
.grow { flex: 1; }
.body { max-width: 1100px; margin: 0 auto; padding: 40px 32px; display: flex; flex-direction: column; gap: 20px; }
.head { display: flex; flex-direction: column; gap: 6px; }
h1 { margin: 0; font-size: 30px; font-weight: 600; letter-spacing: -.01em; }
h2 { margin: 0; font-size: 18px; font-weight: 600; }
.head p { margin: 0; font-size: 15px; }
.import { padding: 14px 18px; gap: 8px; }
.flabel { font-size: 15px; font-weight: 600; color: var(--text); align-self: center; }
.row { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.row .inp { flex: 1 1 320px; min-width: 0; }
.row.end { justify-content: flex-end; }
.hint { margin: 0; font-size: 13px; line-height: 1.55; color: var(--text-muted); }
.hint .mono { color: var(--text-code); }
.empty { font-size: 15px; }
.table { padding: 0; gap: 0; overflow: visible; }
.thead, .proj { display: grid; grid-template-columns: minmax(0, 1.5fr) minmax(0, 1.2fr) minmax(0, 1.4fr) 132px; gap: 16px; align-items: center; padding: 0 20px; }
.thead { padding-top: 10px; padding-bottom: 10px; border-bottom: 1px solid var(--line); font-size: 12.5px; font-weight: 600; color: var(--text-label); text-transform: uppercase; letter-spacing: .07em; }
.list { list-style: none; margin: 0; padding: 0; }
.proj { padding-top: 16px; padding-bottom: 16px; border-bottom: 1px solid var(--line); }
.proj:last-child { border-bottom: 0; }
.pmain { min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.title { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.pname { font-size: 16px; font-weight: 600; color: var(--text); text-decoration: none; overflow-wrap: anywhere; }
a.pname:hover { color: var(--accent-hover); text-decoration: underline; }
.path { font-size: 13px; color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.envs { display: flex; flex-wrap: wrap; gap: 6px; }
.env { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; padding: 3px 10px; border: 1px solid var(--line); border-radius: 999px; background: var(--bg-panel); }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--text-disabled); }
.dot.ok { background: var(--ok); } .dot.warn { background: var(--warn); } .dot.fail { background: var(--fail); } .dot.run { background: var(--running); }
.env .fail { color: var(--fail); } .env .warn { color: var(--warn); } .env .run { color: var(--running); }
.last { min-width: 0; display: flex; flex-direction: column; gap: 2px; font-size: 13.5px; }
.row1 { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.small { font-size: 13px; }
.actions { display: flex; gap: 8px; justify-content: flex-end; }
.actions .mi.dg:not([disabled]) { color: var(--fail); }
.head .fail { color: var(--fail); } .head .warn { color: var(--warn); } .head .run { color: var(--running); }
.pill { font-size: 12.5px; font-weight: 600; padding: 3px 9px; border-radius: 4px; }
.pill.ok { color: var(--ok); background: color-mix(in srgb, var(--ok) 12%, transparent); }
.pill.warn { color: var(--warn); background: color-mix(in srgb, var(--warn) 12%, transparent); }
.pill.fail { color: var(--fail); background: color-mix(in srgb, var(--fail) 12%, transparent); }
.pill.run { color: var(--running); background: color-mix(in srgb, var(--running) 12%, transparent); }
.ver { margin: 0; font-size: 13px; color: var(--text-label); }
.linkish { all: unset; color: var(--accent); cursor: pointer; text-decoration: underline; }
.linkish:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.btn.danger { background: var(--fail); border-color: var(--fail); color: var(--on-accent); }
.dialog { max-width: 520px; width: 100%; }
.dialog p { margin: 0; font-size: 15px; line-height: 1.6; }
@media (max-width: 860px) {
  .thead { display: none; }
  .proj { grid-template-columns: minmax(0, 1fr) auto; }
  .envs, .last { grid-column: 1 / 2; }
  .actions { grid-column: 2; grid-row: 1; }
}
@media (max-width: 640px) { .body { padding: 24px 16px; } .top { padding: 14px 16px; } }
</style>
