<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { serviceApi } from '../api/client'
import type { ProjectView, ServiceInfo } from '../api/types'

const projects = ref<ProjectView[] | null>(null)
const info = ref<ServiceInfo | null>(null)
const source = ref('')
const busy = ref(false)
const error = ref('')
const removing = ref<ProjectView | null>(null)
const removeError = ref('')

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
  if (p.active_runs) return { cls: 'run', text: `${p.active_runs} running` }
  if (p.config_error) return { cls: 'fail', text: 'config error' }
  if (!p.configured) return { cls: 'warn', text: 'needs setup' }
  if (p.open_issues) return { cls: 'warn', text: `${p.open_issues} ${p.open_issues === 1 ? 'issue' : 'issues'}` }
  return { cls: 'ok', text: 'ready' }
}

let timer = 0
const onFocus = () => refresh()
onMounted(async () => {
  document.title = 'Projects · groundwork'
  serviceApi<ServiceInfo>('/service').then((i) => (info.value = i)).catch(() => {})
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
      <div class="logo" aria-hidden="true">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#0D1012" stroke-width="2.4" stroke-linecap="round"><path d="M3 20h18" /><path d="M5 16h14" /><path d="M8 12h8" /><path d="M11 8h2" /></svg>
      </div>
      <span class="mono name">groundwork</span>
      <span class="mono meta">{{ info ? `${info.version} · ${info.home}` : '' }}</span>
    </header>

    <main class="body">
      <div class="head">
        <h1>Projects</h1>
        <p class="muted">Each project is a repository groundwork checks, plans and runs. They all run in this one service.</p>
      </div>

      <form class="card import" @submit.prevent="importProject">
        <label class="field">
          <span class="flabel">Import a repository</span>
          <div class="row">
            <input v-model="source" class="inp" :disabled="busy" placeholder="~/code/infra   or   git@github.com:acme/infra.git" aria-describedby="import-hint" autocomplete="off" spellcheck="false">
            <button class="btn primary" type="submit" :disabled="busy || !source.trim()">
              {{ busy ? (isURL ? 'Cloning…' : 'Importing…') : isURL ? 'Clone and import' : 'Import folder' }}
            </button>
          </div>
        </label>
        <p id="import-hint" class="hint">
          A folder on this machine is used in place; inside a git repository, the repository root is imported.
          A git URL is cloned with your own git and SSH keys<template v-if="info"> into <span class="mono">{{ info.repos_dir }}</span></template>.
          From a terminal, <span class="mono">groundwork</span> in a repository does the same.
        </p>
        <p v-if="error" class="err" role="alert">{{ error }}</p>
      </form>

      <p v-if="projects && !projects.length" class="empty muted">No projects yet. Import one above to get started.</p>

      <ul v-if="projects?.length" class="list" aria-label="Projects">
        <li v-for="p in projects" :key="p.id" class="card proj">
          <div class="main">
            <div class="title">
              <a v-if="p.status === 'ok'" class="pname" :href="`/p/${p.id}/`">{{ p.name }}</a>
              <span v-else class="pname">{{ p.name }}</span>
              <span class="mono pill" :class="status(p).cls">{{ status(p).text }}</span>
            </div>
            <span class="mono path">{{ p.path }}</span>
            <span v-if="p.remote" class="mono path">cloned from {{ p.remote }}</span>
            <span v-if="p.error" class="err">{{ p.error }}</span>
          </div>
          <div class="actions">
            <a v-if="p.status === 'ok'" class="btn sm primary" :href="`/p/${p.id}/`">Open</a>
            <button v-else class="btn sm" type="button" @click="reopen(p)">Retry</button>
            <button class="btn sm" type="button" :aria-label="`Remove ${p.name}`" @click="removing = p; removeError = ''">Remove</button>
          </div>
        </li>
      </ul>
    </main>

    <div v-if="removing" class="scrim" @click.self="removing = null">
      <div class="card dialog" role="dialog" aria-modal="true" aria-labelledby="rm-title">
        <h2 id="rm-title">Remove {{ removing.name }}?</h2>
        <p class="muted">groundwork stops watching it. Files in <span class="mono">{{ removing.path }}</span>, including its <span class="mono">.groundwork/</span> history, are left in place, so importing it again picks up where you left off.</p>
        <p v-if="removeError" class="err" role="alert">{{ removeError }}</p>
        <div class="row end">
          <button class="btn" type="button" @click="removing = null">Cancel</button>
          <button class="btn danger" type="button" @click="confirmRemove">Remove project</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.shell { min-height: 100vh; }
.top { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 16px 32px; border-bottom: 1px solid var(--line); }
.logo { width: 28px; height: 28px; border-radius: 6px; background: var(--accent); display: grid; place-items: center; }
.name { font-weight: 700; }
.meta { margin-left: auto; font-size: 13px; color: var(--text-label); word-break: break-all; }
.body { max-width: 960px; margin: 0 auto; padding: 40px 32px; display: flex; flex-direction: column; gap: 24px; }
.head { display: flex; flex-direction: column; gap: 6px; }
h1 { margin: 0; font-size: 30px; font-weight: 600; letter-spacing: -.01em; }
h2 { margin: 0; font-size: 18px; font-weight: 600; }
.head p { margin: 0; font-size: 15px; }
.flabel { font-size: 15px; font-weight: 600; color: var(--text); }
.row { display: flex; flex-wrap: wrap; gap: 10px; }
.row .inp { flex: 1 1 320px; min-width: 0; }
.row.end { justify-content: flex-end; }
.hint { margin: 0; font-size: 14px; line-height: 1.6; color: var(--text-muted); }
.hint .mono { color: var(--text-code); font-size: 13px; }
.empty { font-size: 15px; }
.list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 12px; }
.proj { flex-direction: row; flex-wrap: wrap; align-items: center; gap: 16px; padding: 18px 22px; }
.main { flex: 1 1 320px; min-width: 0; display: flex; flex-direction: column; gap: 6px; }
.title { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.pname { font-size: 17px; font-weight: 600; color: var(--text); text-decoration: none; }
a.pname:hover { color: var(--accent-hover); }
.path { font-size: 13px; color: var(--text-muted); word-break: break-all; }
.actions { display: flex; gap: 8px; }
.pill { font-size: 12px; font-weight: 500; padding: 3px 9px; border-radius: 4px; }
.pill.ok { color: var(--ok); background: rgba(95, 211, 141, .12); }
.pill.warn { color: var(--warn); background: rgba(245, 182, 71, .12); }
.pill.fail { color: var(--fail); background: rgba(255, 122, 122, .12); }
.pill.run { color: var(--running); background: rgba(124, 192, 255, .12); }
.btn.danger { background: var(--fail); border-color: var(--fail); color: var(--bg); }
.scrim { position: fixed; inset: 0; background: rgba(0, 0, 0, .6); display: grid; place-items: center; padding: 16px; }
.dialog { max-width: 520px; width: 100%; }
.dialog p { margin: 0; font-size: 15px; line-height: 1.6; }
@media (max-width: 640px) { .body { padding: 24px 16px; } .top { padding: 14px 16px; } }
</style>
