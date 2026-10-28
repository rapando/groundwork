<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import AppShell from '../components/AppShell.vue'
import FileTree from '../components/FileTree.vue'
import CodeEditor from '../components/CodeEditor.vue'
import ProblemsTray from '../components/ProblemsTray.vue'
import DiffView from '../components/DiffView.vue'
import ArchitecturePanel from '../components/ArchitecturePanel.vue'
import Modal from '../components/Modal.vue'
import InlineConfirm from '../components/InlineConfirm.vue'
import { api, ApiError } from '../api/client'
import type { Diagnostic, FileContent } from '../api/types'
import { useChecks } from '../stores/checks'
import { useFiles } from '../stores/files'
import { useGit } from '../stores/git'
import { useWorkspace } from '../stores/workspace'
import { onEvent } from '../composables/events'
import { swallow } from '../api/swallow'

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
const isVault = computed(() => !!loaded.value?.content.trimStart().startsWith('$ANSIBLE_VAULT;'))

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

// --- new / delete ---
const creating = ref(false)
const newPath = ref('')
const newError = ref('')
const newBusy = ref(false)
function startNew() {
  // start in the open file's folder, else the first section's
  const base = path.value ? dirOf(path.value) : (files.sections.find((s) => s.base !== '.')?.base ?? '')
  newPath.value = base && !base.endsWith('/') ? base + '/' : base
  newError.value = ''
  creating.value = true
}
async function createFile() {
  const p = newPath.value.trim().replace(/^\.?\/+/, '')
  if (!p || p.endsWith('/')) { newError.value = 'Enter a file name, e.g. terraform/envs/dev/network.tf'; return }
  newBusy.value = true
  newError.value = ''
  try {
    await api('/files/content?path=' + encodeURIComponent(p), { method: 'PUT', body: { content: '', create: true } })
    creating.value = false
    await files.refresh().catch(swallow('file list refresh after create'))
    open(p)
  } catch (e) {
    newError.value = e instanceof ApiError && e.code === 'exists' ? 'A file with that name already exists.' : e instanceof Error ? e.message : String(e)
  } finally {
    newBusy.value = false
  }
}

const deleting = ref(false)
// say what can and can't be got back: git has committed files, nothing has new ones
const deleteMessage = computed(() => {
  const st = gitFile.value?.status
  const back = !git.status?.available ? ' This cannot be undone.'
    : !st ? ' Git still has it, so it can be restored.'
    : st === 'untracked' || st === 'added' ? ' It was never committed, so it cannot be restored.'
      : ' Git can restore the last committed version; uncommitted changes are lost.'
  return `Delete ${path.value}?${back}` + (dirty.value ? ' Your unsaved edits go with it.' : '')
})
const deleteBusy = ref(false)
async function deleteFile() {
  if (!loaded.value) return
  const p = path.value
  deleteBusy.value = true
  try {
    await api('/files/content?path=' + encodeURIComponent(p), { method: 'DELETE', headers: { 'If-Match': loaded.value.sha } })
    deleting.value = false
    loaded.value = null // nothing left to lose, so leaving doesn't ask
    buffer.value = ''
    await router.push('/code')
    say('Deleted ' + p)
    files.refresh().catch(swallow('file list refresh after delete'))
    git.refresh().catch(swallow('git status refresh after delete'))
  } catch (e) {
    deleting.value = false
    if (e instanceof ApiError && e.code === 'conflict') conflict.value = { currentSha: (e.details as { current_sha: string }).current_sha }
    else say('Delete failed: ' + (e instanceof Error ? e.message : String(e)))
  } finally {
    deleteBusy.value = false
  }
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
// what to offer when nothing is open: uncommitted files first, then the usual entry points
const uncommitted = computed(() => git.status?.files ?? [])
const starters = computed(() => {
  const all = files.files
  const pick = ['groundwork.yaml', ...all.filter((f) => /(^|\/)(main\.tf|site\.ya?ml|playbook\.ya?ml)$/.test(f))].filter((f, i, a) => all.includes(f) && a.indexOf(f) === i)
  return pick.slice(0, 4)
})
const dirOf = (p: string) => (p.includes('/') ? p.slice(0, p.lastIndexOf('/') + 1) : '')
const baseOf = (p: string) => p.slice(p.lastIndexOf('/') + 1)
const openPalette = () => window.dispatchEvent(new Event('gw:palette'))
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

// The other .tf files of the module, so completions know its variables,
// locals, modules and resources. Loaded once per file opened.
const siblings = ref<string[]>([])
// Re-read when the file list changes, since it may not have arrived yet.
watch([path, () => files.files], async ([p]) => {
  siblings.value = []
  if (!/\.tf$/.test(p)) return
  const dir = p.slice(0, p.lastIndexOf('/') + 1)
  const others = files.files.filter((f) => f !== p && f.startsWith(dir) && /\.tf$/.test(f) && !f.slice(dir.length).includes('/'))
  const texts = await Promise.all(others.slice(0, 50).map((f) =>
    api<FileContent>('/files/content?path=' + encodeURIComponent(f)).then((c) => c.content, () => '')))
  if (p === path.value) siblings.value = texts
}, { immediate: true })
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
  if (v && !visualizeRecorded) { visualizeRecorded = true; api('/onboarding/event', { body: { name: 'visualize.opened' } }).catch(swallow('onboarding event')) }
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
        <span v-if="!crumbs.length" class="muted">No file open</span>
        <span v-if="dirty" class="mod" role="status">● modified</span>
        <span v-else-if="gitFile" class="mod git" role="status">● uncommitted</span>
      </nav>
      <div v-if="path" class="actions">
        <div v-if="canVisualize" class="seg-group" role="group" aria-label="View">
          <button class="seg" :class="{ on: viewMode === 'code' }" type="button" :aria-pressed="viewMode === 'code'" @click="setMode('code')">Code</button>
          <button class="seg" :class="{ on: viewMode === 'split' }" type="button" :aria-pressed="viewMode === 'split'" @click="setMode('split')">Split</button>
          <button class="seg" :class="{ on: viewMode === 'visual' }" type="button" :aria-pressed="viewMode === 'visual'" @click="setMode('visual')">Visualize</button>
        </div>
        <button class="btn sm" type="button" :disabled="!loaded || !dirty || saving" @click="save()">Save <span class="mono kbd">⌘S</span></button>
        <button v-if="isTerraform" class="btn sm" type="button" :disabled="!loaded" @click="format()">Format</button>
        <button class="btn sm" type="button" :disabled="!loaded || checks.running" @click="runChecks">{{ checks.running ? 'Checking…' : 'Validate' }}</button>
        <button class="btn sm" type="button" :disabled="!gitFile || dirty" :title="dirty ? 'Save first to diff' : ''" :aria-pressed="diffOpen" @click="toggleDiff">{{ diffOpen ? 'Back to editor' : 'Diff' }}</button>
        <button class="btn sm" type="button" :disabled="!loaded || deleting" @click="deleting = true">Delete</button>
      </div>
    </header>

    <div v-if="deleting" class="confirm">
      <InlineConfirm
        danger :busy="deleteBusy" confirm-label="Delete file"
        :message="deleteMessage"
        @confirm="deleteFile" @cancel="deleting = false"
      />
    </div>

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

    <div v-if="isVault && !conflict && !externalChange" class="banner info" role="note">
      This file is encrypted with ansible-vault.
      <RouterLink class="btn sm" :to="{ path: '/variables', query: { tab: 'secrets', edit: path } }">Edit decrypted</RouterLink>
    </div>

    <div class="work">
      <FileTree
        class="tree"
        :files="files.files" :sections="files.sections" :selected="path" :badges="checks.badges" :git="git.byPath"
        :iac-only="files.iacOnly" @open="open" @iac="files.setIacOnly($event)" @new="startNew"
      />
      <div class="pane">
        <div class="editor-area" :class="{ split: showVisual && showEditor, visualonly: showVisual && !showEditor }">
          <section v-if="!path" class="start" aria-label="Open a file">
            <p v-if="notice" class="notice-start" role="status">{{ notice }}</p>
            <div><h2>Open a file</h2>
              <p class="muted">Pick one from the tree, press <kbd class="mono">⌘K</kbd> to <button class="link" type="button" @click="openPalette">search every file by name</button>, or <button class="link" type="button" @click="startNew">create a new file</button>.</p></div>
            <div v-if="uncommitted.length">
              <div class="shead"><span class="lbl">Uncommitted · {{ git.dirty }}</span></div>
              <ul class="flist">
                <li v-for="f in uncommitted.slice(0, 8)" :key="f.path">
                  <button class="frow" type="button" @click="open(f.path)"><span class="mono"><span class="dir">{{ dirOf(f.path) }}</span>{{ baseOf(f.path) }}</span><span class="mono st">{{ f.status }}</span></button>
                </li>
              </ul>
              <p v-if="uncommitted.length > 8" class="small muted more">and {{ uncommitted.length - 8 }} more, marked in the tree</p>
            </div>
            <div v-if="starters.length">
              <span class="lbl">Start from</span>
              <div class="starters"><button v-for="f in starters" :key="f" class="btn sm mono" type="button" @click="open(f)">{{ f }}</button></div>
            </div>
          </section>
          <p v-else-if="loading" class="state muted">Loading…</p>
          <div v-else-if="loadError" class="state" role="alert">
            <p class="fail">{{ loadError.message }}</p>
            <p v-if="loadError.code === 'not_found'" class="muted">It may have been deleted or renamed.</p>
          </div>
          <DiffView v-else-if="diffOpen" :diff="diffText" />
          <template v-else-if="loaded">
            <div v-show="showEditor" class="edpane">
              <CodeEditor
                ref="editor" :path="path" :doc="loaded.content" :version="version" :diagnostics="fileDiags" :siblings="siblings"
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

    <Modal v-if="creating" labelledby="new-title" @close="creating = false">
      <form class="card" @submit.prevent="createFile">
        <h2 id="new-title" class="mtitle">New file</h2>
        <label class="field">Path in the repository
          <input v-model="newPath" class="inp" :class="{ bad: newError }" type="text" spellcheck="false" autocomplete="off" placeholder="terraform/envs/dev/network.tf" :aria-invalid="!!newError" aria-describedby="new-hint" />
        </label>
        <p v-if="newError" class="err" role="alert">{{ newError }}</p>
        <p v-else id="new-hint" class="muted small">Folders that don't exist yet are created.</p>
        <div class="mrow">
          <button class="btn" type="button" @click="creating = false">Cancel</button>
          <button class="btn primary" type="submit" :disabled="newBusy">Create</button>
        </div>
      </form>
    </Modal>
  </AppShell>
</template>

<style scoped>
.head { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; min-height: 59px; padding: 12px 24px; border-bottom: 1px solid var(--line); }
.crumbs { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; font-size: 14px; color: var(--text-label); min-width: 0; }
.crumbs .cur { color: var(--text); }
.mod { margin-left: 6px; font-size: 13px; color: var(--warn); }
.mod.git { color: var(--text-label); }
.actions { margin-left: auto; display: flex; flex-wrap: wrap; gap: 8px; }
.kbd { font-size: 13px; color: var(--text-label); }
.banner { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 10px 24px; font-size: 14px; border-bottom: 1px solid var(--line); }
.banner.fail { background: var(--fail-bg); color: var(--fail-strong); }
.banner.warn { background: var(--warn-bg); color: var(--warn-strong); }
.banner.info { background: var(--bg-raised); color: var(--text-soft); }
/* one row on desktop, so the pane has a definite height and the editor scrolls inside it */
.work { display: flex; flex: 1; min-height: 0; }
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
.start { max-width: 760px; padding: 48px 56px; display: flex; flex-direction: column; gap: 28px; }
.start h2 { margin: 0 0 6px; font-size: 22px; font-weight: 600; }
.start p { margin: 0; }
.start kbd { font-size: 12.5px; border: 1px solid var(--line-strong); border-radius: 4px; padding: 1px 6px; }
.shead { margin-bottom: 8px; }
.flist { list-style: none; margin: 0; padding: 0; border: 1px solid var(--line); border-radius: 10px; background: var(--bg-card); overflow: hidden; }
.flist li + li { border-top: 1px solid var(--line); }
.frow { all: unset; box-sizing: border-box; width: 100%; display: flex; justify-content: space-between; gap: 12px; padding: 9px 16px; font-size: 13.5px; cursor: pointer; }
.frow:hover { background: var(--bg-hover); }
.frow:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
.frow .dir { color: var(--text-faint); }
.frow .st { color: var(--ok); }
.more { margin-top: 8px !important; }
.starters { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 8px; }
.start .small { font-size: 13px; }
.start .link { background: none; border: 0; padding: 0; font: inherit; color: var(--accent); cursor: pointer; text-decoration: underline; }
.status { padding: 6px 16px; font-size: 13px; color: var(--text-faint); border-top: 1px solid var(--bg-hover); display: flex; gap: 12px; }
.notice { margin-left: auto; color: var(--ok); }
.notice-start { color: var(--ok); font-size: 14px; }
.confirm { padding: 10px 24px; border-bottom: 1px solid var(--line); }
.mtitle { margin: 0; font-size: 18px; font-weight: 600; }
.card .inp { width: 100%; }
.card .small { margin: 0; font-size: 13px; }
.card .err { margin: 0; }
.mrow { display: flex; justify-content: flex-end; gap: 8px; }
:deep(.tray) { max-height: 40vh; }
@media (max-width: 900px) { .work { flex-wrap: wrap; } .tree { max-width: 100%; max-height: 260px; } }
</style>
