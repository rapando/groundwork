<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import AppShell from '../components/AppShell.vue'
import FileTree from '../components/FileTree.vue'
import CodeEditor from '../components/CodeEditor.vue'
import ProblemsTray from '../components/ProblemsTray.vue'
import DiffView from '../components/DiffView.vue'
import ArchitecturePanel from '../components/ArchitecturePanel.vue'
import { api, ApiError } from '../api/client'
import type { Diagnostic, FileContent } from '../api/types'
import { useChecks } from '../stores/checks'
import { useFiles } from '../stores/files'
import { useGit } from '../stores/git'
import { useWorkspace } from '../stores/workspace'
import { onEvent } from '../composables/events'

const route = useRoute()
const router = useRouter()
const files = useFiles()
const checks = useChecks()
const git = useGit()
const ws = useWorkspace()

const editor = ref<InstanceType<typeof CodeEditor>>()

const path = computed(() => {
  const p = route.params.path
  return Array.isArray(p) ? p.join('/') : (p as string) || ''
})

// --- buffer state ---
const loaded = ref<FileContent | null>(null) // what is on disk, as last read
const buffer = ref('')
const version = ref(0)
const loading = ref(false)
const loadError = ref<{ code: string; message: string } | null>(null)
const saving = ref(false)
const conflict = ref<{ currentSha: string } | null>(null)
const externalChange = ref(false)
const notice = ref('')
const cursor = ref({ line: 1, col: 1 })
const diffOpen = ref(false)
const diffText = ref('')
const dirty = computed(() => !!loaded.value && buffer.value !== loaded.value.content)

let noticeTimer: number | undefined
function say(msg: string) {
  notice.value = msg
  window.clearTimeout(noticeTimer)
  noticeTimer = window.setTimeout(() => (notice.value = ''), 5000)
}

async function load(p: string) {
  loadError.value = null
  conflict.value = null
  externalChange.value = false
  diffOpen.value = false
  loaded.value = null
  buffer.value = ''
  if (!p) return
  loading.value = true
  try {
    const c = await api<FileContent>('/files/content?path=' + encodeURIComponent(p))
    if (p !== path.value) return // navigated away meanwhile
    loaded.value = c
    buffer.value = c.content
    version.value++
    const line = Number(route.query.line)
    if (line > 0) setTimeout(() => editor.value?.goTo(line, 1), 0)
  } catch (e) {
    if (p !== path.value) return
    loadError.value = e instanceof ApiError ? { code: e.code, message: e.message } : { code: 'error', message: String(e) }
  } finally {
    loading.value = false
  }
}

async function save(overwrite = false): Promise<boolean> {
  if (!loaded.value || saving.value) return false
  saving.value = true
  try {
    const sha = overwrite && conflict.value ? conflict.value.currentSha : loaded.value.sha
    const text = buffer.value
    const r = await api<{ sha: string }>('/files/content?path=' + encodeURIComponent(path.value), {
      method: 'PUT', body: { content: text }, headers: { 'If-Match': sha },
    })
    loaded.value = { ...loaded.value, content: text, sha: r.sha }
    conflict.value = null
    externalChange.value = false
    say('Saved · checks will re-run')
    return true
  } catch (e) {
    if (e instanceof ApiError && e.code === 'conflict') {
      conflict.value = { currentSha: (e.details as { current_sha: string }).current_sha }
    } else say('Save failed: ' + (e instanceof Error ? e.message : String(e)))
    return false
  } finally {
    saving.value = false
  }
}

async function reloadFromDisk() {
  await load(path.value)
}

// --- external changes ---
const offEvent = onEvent('file.changed', async (d: { paths?: string[] }) => {
  if (!path.value || !d?.paths?.includes(path.value) || !loaded.value) return
  try {
    const c = await api<FileContent>('/files/content?path=' + encodeURIComponent(path.value))
    if (c.sha === loaded.value.sha) return // our own save
    if (!dirty.value) {
      loaded.value = c
      buffer.value = c.content
      version.value++
      say('Reloaded: the file changed on disk')
    } else {
      externalChange.value = true
    }
  } catch { /* deleted or unreadable: leave the buffer alone */ }
})
onBeforeUnmount(offEvent)

// --- checks / fixes ---
const unit = computed(() => checks.unitFor(path.value))
const fileDiags = computed(() => checks.forFile(path.value))
const isTerraform = computed(() => /\.(tf|tfvars)$/.test(path.value))

function gotoDiag(d: Diagnostic) {
  if (d.file !== path.value) {
    open(d.file)
    const stop = watch(loading, (l) => { if (!l) { stop(); setTimeout(() => d.line && editor.value?.goTo(d.line, d.col ?? 1), 0) } })
    return
  }
  if (d.line) editor.value?.goTo(d.line, d.col ?? 1)
}

async function applyFix(d: Diagnostic) {
  const fix = d.fix
  if (!fix) return
  if (fix.kind === 'edit' && fix.edits?.length) {
    if (d.file !== path.value) return gotoDiag(d)
    const ok = fix.edits.every((e) => editor.value?.applyEdit(e))
    say(ok ? 'Fix applied to the buffer. Save to keep it.' : 'The text changed since the check ran; re-run checks to refresh this fix.')
  } else if (fix.kind === 'fmt' && fix.file) {
    await format(fix.file)
  }
}

async function format(file = path.value) {
  if (file === path.value && dirty.value && !(await save())) return
  try {
    await api('/checks/fix', { body: { kind: 'fmt', file } })
    if (file === path.value) await reloadFromDisk()
    say('Formatted')
  } catch (e) { say(e instanceof Error ? e.message : String(e)) }
}

async function runChecks() {
  if (dirty.value) await save()
  try { await checks.run(unit.value?.id ?? '', true) } catch (e) { say(e instanceof Error ? e.message : String(e)) }
}

// --- diff ---
const gitFile = computed(() => git.byPath.get(path.value))
async function toggleDiff() {
  if (diffOpen.value) { diffOpen.value = false; return }
  try {
    const r = await api<{ diff: string }>('/git/diff?path=' + encodeURIComponent(path.value))
    diffText.value = r.diff
    diffOpen.value = true
  } catch (e) { say(e instanceof Error ? e.message : String(e)) }
}

// --- navigation ---
function open(p: string) {
  router.push('/code/' + p.split('/').map(encodeURIComponent).join('/'))
}
function confirmLeave(): boolean {
  return !dirty.value || window.confirm('You have unsaved changes. Discard them?')
}
onBeforeRouteUpdate(() => confirmLeave())
onBeforeRouteLeave(() => confirmLeave())
function beforeUnload(e: BeforeUnloadEvent) { if (dirty.value) { e.preventDefault(); e.returnValue = '' } }
onMounted(() => window.addEventListener('beforeunload', beforeUnload))
onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload))

watch(path, (p) => load(p), { immediate: true })
onMounted(async () => {
  await Promise.all([files.refresh(), checks.refresh(), git.refresh()])
})

const crumbs = computed(() => path.value.split('/').filter(Boolean))
const langName = computed(() => {
  const p = path.value
  if (/\.(tf|tfvars|hcl)$/.test(p)) return 'HCL'
  if (/\.ya?ml$/.test(p)) return 'YAML'
  if (p.endsWith('.json')) return 'JSON'
  if (/\.(cfg|ini)$/.test(p)) return 'INI'
  return 'Text'
})
const onSave = computed(() => ws.ws?.config?.checks?.on_save !== false)

// Code / Split / Visualize (Terraform files only)
type ViewMode = 'code' | 'split' | 'visual'
function savedMode(): ViewMode {
  try { const v = localStorage.getItem('gw.codeView'); return v === 'split' || v === 'visual' ? v : 'code' } catch { return 'code' }
}
const viewMode = ref<ViewMode>(savedMode())
function setMode(m: ViewMode) { viewMode.value = m; try { localStorage.setItem('gw.codeView', m) } catch { /* ignore */ } }
const canVisualize = computed(() => /\.tf$/.test(path.value))
const showEditor = computed(() => !canVisualize.value || viewMode.value !== 'visual')
const showVisual = computed(() => canVisualize.value && viewMode.value !== 'code' && !!loaded.value && !diffOpen.value)
let visualizeRecorded = false // for the Getting started checklist
watch(showVisual, (v) => {
  if (v && !visualizeRecorded) { visualizeRecorded = true; api('/onboarding/event', { body: { name: 'visualize.opened' } }).catch(() => {}) }
}, { immediate: true })
function gotoFrom(file: string, line: number) {
  if (file === path.value) {
    if (viewMode.value === 'visual') setMode('split')
    setTimeout(() => editor.value?.goTo(line, 1), 0)
  } else {
    router.push(`/code/${file.split('/').map(encodeURIComponent).join('/')}?line=${line}`)
  }
}
</script>

<template>
  <AppShell>
    <header class="head">
      <nav aria-label="Breadcrumb" class="crumbs mono">
        <template v-for="(c, i) in crumbs" :key="i">
          <span v-if="i" class="sep">/</span>
          <span :class="{ cur: i === crumbs.length - 1 }">{{ c }}</span>
        </template>
        <span v-if="!crumbs.length" class="muted">Select a file</span>
        <span v-if="dirty" class="mod" role="status">● modified</span>
        <span v-else-if="gitFile" class="mod git" role="status">● uncommitted</span>
      </nav>
      <div class="actions">
        <div v-if="canVisualize" class="seg-group" role="group" aria-label="View">
          <button class="seg" :class="{ on: viewMode === 'code' }" type="button" :aria-pressed="viewMode === 'code'" @click="setMode('code')">Code</button>
          <button class="seg" :class="{ on: viewMode === 'split' }" type="button" :aria-pressed="viewMode === 'split'" @click="setMode('split')">Split</button>
          <button class="seg" :class="{ on: viewMode === 'visual' }" type="button" :aria-pressed="viewMode === 'visual'" @click="setMode('visual')">Visualize</button>
        </div>
        <button class="btn sm" type="button" :disabled="!loaded || !dirty || saving" @click="save()">Save <span class="mono kbd">⌘S</span></button>
        <button v-if="isTerraform" class="btn sm" type="button" :disabled="!loaded" @click="format()">Format</button>
        <button class="btn sm" type="button" :disabled="!loaded || checks.running" @click="runChecks">{{ checks.running ? 'Checking…' : 'Validate' }}</button>
        <button class="btn sm" type="button" :disabled="!gitFile || dirty" :title="dirty ? 'Save first to diff' : ''" :aria-pressed="diffOpen" @click="toggleDiff">{{ diffOpen ? 'Back to editor' : 'Diff' }}</button>
      </div>
    </header>

    <div v-if="conflict" class="banner fail" role="alert">
      This file changed on disk after you opened it. Saving would overwrite those changes.
      <button class="btn sm" type="button" @click="reloadFromDisk">Reload (discard my edits)</button>
      <button class="btn sm" type="button" @click="save(true)">Overwrite anyway</button>
    </div>
    <div v-else-if="externalChange" class="banner warn" role="alert">
      This file was changed on disk while you have unsaved edits.
      <button class="btn sm" type="button" @click="reloadFromDisk">Reload (discard my edits)</button>
      <button class="btn sm" type="button" @click="externalChange = false">Keep mine</button>
    </div>

    <div class="work">
      <FileTree
        class="tree"
        :files="files.files" :sections="files.sections" :selected="path" :badges="checks.badges" :git="git.byPath"
        :iac-only="files.iacOnly" @open="open" @iac="files.setIacOnly($event)"
      />
      <div class="pane">
        <div class="editor-area" :class="{ split: showVisual && showEditor, visualonly: showVisual && !showEditor }">
          <p v-if="!path" class="state muted">Choose a file from the tree to start editing.</p>
          <p v-else-if="loading" class="state muted">Loading…</p>
          <div v-else-if="loadError" class="state" role="alert">
            <p class="fail">{{ loadError.message }}</p>
            <p v-if="loadError.code === 'not_found'" class="muted">It may have been deleted or renamed.</p>
          </div>
          <DiffView v-else-if="diffOpen" :diff="diffText" />
          <template v-else-if="loaded">
            <div v-show="showEditor" class="edpane">
              <CodeEditor
                ref="editor" :path="path" :doc="loaded.content" :version="version" :diagnostics="fileDiags"
                @change="buffer = $event" @save="save()" @cursor="cursor = $event" @fix="applyFix"
              />
            </div>
            <ArchitecturePanel v-if="showVisual" class="vispane" :path="path" :buffer="buffer" :cursor-line="cursor.line" @goto="gotoFrom" />
          </template>
        </div>
        <div v-if="loaded && !diffOpen" class="status mono">
          Ln {{ cursor.line }}, Col {{ cursor.col }} · {{ langName }} · {{ onSave ? 'edits re-validate on save' : 'checks run manually' }}
          <span v-if="notice" class="notice" role="status">{{ notice }}</span>
        </div>
        <ProblemsTray
          v-if="loaded"
          :file="path" :file-diagnostics="fileDiags" :all-diagnostics="checks.diagnostics" :unit="unit" :running="!!unit?.running || checks.running"
          @goto="gotoDiag" @fix="applyFix" @run="runChecks"
        />
      </div>
    </div>
  </AppShell>
</template>

<style scoped>
.head { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 12px 24px; border-bottom: 1px solid var(--line); }
.crumbs { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; font-size: 14px; color: var(--text-label); min-width: 0; }
.crumbs .cur { color: var(--text); }
.mod { margin-left: 6px; font-size: 12px; color: var(--warn); }
.mod.git { color: var(--text-label); }
.actions { margin-left: auto; display: flex; flex-wrap: wrap; gap: 8px; }
.kbd { font-size: 12px; color: var(--text-label); }
.banner { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 10px 24px; font-size: 14px; border-bottom: 1px solid var(--line); }
.banner.fail { background: var(--fail-bg); color: var(--fail-strong); }
.banner.warn { background: var(--warn-bg); color: var(--warn-strong); }
.work { display: flex; flex-wrap: wrap; flex: 1; min-height: 0; }
.tree { flex: 1 1 248px; max-width: 320px; border-right: 1px solid var(--line); }
.pane { flex: 999 1 480px; min-width: 0; display: flex; flex-direction: column; min-height: 0; }
.editor-area { flex: 1; min-height: 240px; min-width: 0; display: flex; }
.edpane { flex: 1 1 50%; min-width: 0; min-height: 0; }
.vispane { flex: 1 1 50%; min-width: 0; }
.editor-area.visualonly .vispane { flex-basis: 100%; }
.seg-group { display: flex; gap: 2px; padding: 2px; border: 1px solid var(--line-strong); border-radius: 7px; background: var(--bg-card); }
.seg { font-size: 14px; height: 30px; padding: 0 12px; border: 0; border-radius: 5px; background: transparent; color: var(--text-muted); cursor: pointer; }
.seg.on { background: var(--line-strong); color: var(--text); }
.state { padding: 24px; margin: 0; }
.status { padding: 6px 16px; font-size: 12.5px; color: var(--text-faint); border-top: 1px solid var(--bg-hover); display: flex; gap: 12px; }
.notice { margin-left: auto; color: var(--ok); }
:deep(.tray) { max-height: 40vh; }
@media (max-width: 900px) { .tree { max-width: 100%; max-height: 260px; } }
</style>
