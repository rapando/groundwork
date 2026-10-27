<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from '../components/AppShell.vue'
import DiffView from '../components/DiffView.vue'
import { api, ApiError } from '../api/client'
import type { AnsibleVarsResponse, MatrixRow, RekeyPlan, SecretFinding, SecretItem, SecretsResponse, TerraformVarsResponse, VarCell, VarRow } from '../api/types'
import Modal from '../components/Modal.vue'

type Tab = 'terraform' | 'ansible' | 'secrets'
const route = useRoute()
const router = useRouter()
const tab = computed<Tab>(() => (['terraform', 'ansible', 'secrets'].includes(String(route.query.tab)) ? route.query.tab : 'terraform') as Tab)
const setTab = (t: Tab) => router.replace({ query: { ...route.query, tab: t } })

const tf = ref<TerraformVarsResponse | null>(null)
const ans = ref<AnsibleVarsResponse | null>(null)
const sec = ref<SecretsResponse | null>(null)
const findings = ref<SecretFinding[] | null>(null)
const error = ref('')
const q = ref('')
const onlyDiffer = ref(false)

const msg = (e: unknown) => (e instanceof Error ? e.message : String(e))

async function loadTF() {
  const s = route.query.stack ? '?stack=' + encodeURIComponent(String(route.query.stack)) : ''
  try { tf.value = await api<TerraformVarsResponse>('/vars/terraform' + s) } catch (e) { error.value = msg(e) }
}
async function loadAns() {
  const p = route.query.project ? '?project=' + encodeURIComponent(String(route.query.project)) : ''
  try { ans.value = await api<AnsibleVarsResponse>('/vars/ansible' + p) } catch (e) { error.value = msg(e) }
}
const secLoading = ref(false)
async function loadSecrets() {
  secLoading.value = true
  try { sec.value = await api<SecretsResponse>('/secrets') } catch (e) { error.value = msg(e) } finally { secLoading.value = false }
}
const scanning = ref(false)
async function scan() {
  scanning.value = true
  try { findings.value = (await api<{ findings: SecretFinding[] }>('/secrets/scan', { method: 'POST' })).findings } catch (e) { error.value = msg(e) } finally { scanning.value = false }
}
onMounted(() => { loadTF(); loadAns(); loadSecrets(); scan() })
watch(() => route.query.stack, () => { selected.value = ''; loadTF() })
watch(() => route.query.project, loadAns)

const match = (name: string) => !q.value || name.toLowerCase().includes(q.value.toLowerCase())
const tfRows = computed(() => (tf.value?.rows ?? []).filter((r) => match(r.name) && (!onlyDiffer.value || r.differs || r.missing)))
const tfEnvs = computed(() => tf.value?.stacks.find((s) => s.id === tf.value?.stack)?.targets ?? [])
const ansRows = computed(() => (ans.value?.matrix?.rows ?? []).filter((r) => (match(r.name) || match(r.group)) && (!onlyDiffer.value || r.differs || r.missing)))
const counts = computed(() => ({
  terraform: tf.value?.rows.length ?? 0,
  ansible: ans.value?.matrix?.rows?.length ?? 0,
  secrets: (sec.value?.secrets.length ?? 0) + (findings.value?.length ?? 0),
}))

const CELL_CLASS: Record<string, string> = { set: 'c-set', default: 'c-def', env: 'c-sec', secret: 'c-sec', missing: 'c-miss', absent: 'c-abs', vault: 'c-sec' }
const cellText = (c: VarCell) => (c.state === 'missing' ? 'missing' : c.state === 'absent' ? 'not declared' : c.value)
const codeHref = (f: string, line?: number) => ({ path: '/code/' + f.split('/').map(encodeURIComponent).join('/'), query: line ? { line: String(line) } : {} })

// ---- Terraform detail + editor ----
const selected = ref('')
const selEnv = ref('')
const row = computed<VarRow | undefined>(() => tf.value?.rows.find((r) => r.name === selected.value))
const cell = computed(() => row.value?.cells.find((c) => c.env === selEnv.value))
function select(r: VarRow, env?: string) {
  selected.value = r.name
  const target = env ?? r.cells.find((c) => c.state === 'missing')?.env ?? r.cells[0]?.env ?? ''
  selEnv.value = target
}
const edit = reactive({ value: '', file: '', diff: '', sha: '', create: false, error: '', saving: false, saved: '' })
watch([selected, selEnv], () => {
  const c = cell.value
  Object.assign(edit, { value: c && (c.state === 'set' || c.state === 'default') ? c.value : '', file: c?.file && c.writable.includes(c.file) ? c.file : c?.writable[0] ?? '', diff: '', sha: '', error: '', saved: '' })
  reveal.value = null
})
const missingIn = computed(() => row.value?.cells.filter((c) => c.state === 'missing').map((c) => c.env) ?? [])
async function previewEdit() {
  const c = cell.value
  if (!c || !row.value) return
  edit.error = ''; edit.saved = ''
  try {
    const r = await api<{ diff: string; sha: string; create: boolean }>('/vars/terraform', { method: 'PUT', body: { root: c.root, env: c.env, name: row.value.name, value: edit.value, file: edit.file } })
    Object.assign(edit, { diff: r.diff, sha: r.sha, create: r.create })
  } catch (e) { edit.error = msg(e) }
}
async function saveEdit() {
  const c = cell.value
  if (!c || !row.value) return
  edit.saving = true; edit.error = ''
  try {
    await api('/vars/terraform', { method: 'PUT', body: { root: c.root, env: c.env, name: row.value.name, value: edit.value, file: edit.file, apply: true, sha: edit.sha } })
    edit.saved = `Saved to ${edit.file}. Validation is running; problems show in the Code screen.`
    edit.diff = ''
    await loadTF()
  } catch (e) {
    edit.error = e instanceof ApiError && e.status === 409 ? 'The file changed since the preview. Preview again.' : msg(e)
  } finally { edit.saving = false }
}

// ---- reveal: one value, held in memory for 30 s, never stored ----
const reveal = ref<{ key: string; value: string } | null>(null)
const revealErr = ref('')
let revealTimer: number | undefined
function showReveal(key: string, value: string) {
  reveal.value = { key, value }
  window.clearTimeout(revealTimer)
  revealTimer = window.setTimeout(() => (reveal.value = null), 30_000)
}
onBeforeUnmount(() => { window.clearTimeout(revealTimer); reveal.value = null })
async function revealTF() {
  const c = cell.value
  if (!c || !row.value) return
  revealErr.value = ''
  try {
    const r = await api<{ value: string }>('/secrets/reveal', { body: { root: c.root, env: c.env, name: row.value.name } })
    showReveal('tf:' + row.value.name + c.env, r.value)
    loadSecrets()
  } catch (e) { revealErr.value = msg(e) }
}
async function revealSecret(s: SecretItem) {
  revealErr.value = ''
  try {
    const r = await api<{ value: string }>('/secrets/reveal', { body: { id: s.id } })
    showReveal(s.id, r.value)
    loadSecrets()
  } catch (e) { revealErr.value = msg(e) }
}

const STATE: Record<string, [string, string]> = {
  'can-decrypt': ['st-ok', 'can decrypt'], 'wrong-password': ['st-fail', 'wrong password'], 'no-password': ['st-warn', 'no password'], locked: ['st-warn', 'locked'],
  'tool-missing': ['st-warn', 'tool missing'], 'cannot-decrypt': ['st-fail', "can't decrypt"], 'names-only': ['st-idle', 'name only'], unchecked: ['st-idle', 'not checked'],
}
const secrets = computed(() => (sec.value?.secrets ?? []).filter((s) => match(s.name) || match(s.file ?? '')))

// ---- move a plaintext finding to ansible-vault ----
const move = reactive({ f: null as SecretFinding | null, diff: '', sha: '', error: '', note: '', busy: false })
const canMove = (f: SecretFinding) => /\.ya?ml$/.test(f.file)
async function previewMove(f: SecretFinding) {
  Object.assign(move, { f, diff: '', sha: '', error: '', note: '' })
  try {
    const r = await api<{ diff: string; sha: string }>('/secrets/move', { body: { file: f.file, line: f.line } })
    Object.assign(move, { diff: r.diff, sha: r.sha })
  } catch (e) { move.error = msg(e) }
}
async function applyMove() {
  if (!move.f) return
  move.busy = true; move.error = ''
  try {
    const r = await api<{ note: string }>('/secrets/move', { body: { file: move.f.file, line: move.f.line, apply: true, sha: move.sha } })
    Object.assign(move, { f: null, diff: '', note: r.note })
    await Promise.all([scan(), loadSecrets(), loadAns()])
  } catch (e) { move.error = msg(e) } finally { move.busy = false }
}
const ansRowLabel = (r: MatrixRow) => (r.group === 'all' ? r.name : `${r.group} · ${r.name}`)

// ---- ansible-vault: edit, create, add, encrypt, rekey ----
// Plaintext lives in these refs only while a dialog is open.
const vaultProjects = computed(() => sec.value?.vault ?? [])
const isVault = (s: SecretItem) => s.kind === 'vault-file' || s.kind === 'vault-inline'
const vaultNote = ref('')
const conflictMsg = (e: unknown) => (e instanceof ApiError && e.status === 409 ? 'The file changed since you opened it. Close and try again.' : msg(e))
async function afterVaultWrite(note: string) {
  vaultNote.value = note
  await Promise.all([loadSecrets(), loadAns(), scan()])
}
// the identities of the Ansible project a repository path belongs to
function idsFor(file: string) {
  let best: { path: string; ids: { label: string }[] } | undefined
  for (const p of vaultProjects.value) {
    const inside = p.path === '.' || file === p.path || file.startsWith(p.path + '/')
    if (inside && (!best || p.path.length > best.path.length)) best = p
  }
  return best?.ids ?? []
}

const ved = reactive({ s: null as SecretItem | null, value: '', sha: '', loading: false, busy: false, error: '' })
async function openVaultEdit(s: SecretItem) {
  Object.assign(ved, { s, value: '', sha: '', error: '', loading: true })
  try {
    const r = await api<{ value: string; sha: string }>('/secrets/reveal', { body: { id: s.id } })
    Object.assign(ved, { value: r.value, sha: r.sha })
  } catch (e) { ved.error = msg(e) } finally { ved.loading = false; loadSecrets() }
}
const closeVaultEdit = () => Object.assign(ved, { s: null, value: '', sha: '', error: '' })
async function saveVaultEdit() {
  if (!ved.s) return
  ved.busy = true; ved.error = ''
  try {
    const r = await api<{ changed: boolean; file: string }>('/secrets/vault/update', { body: { id: ved.s.id, value: ved.value, sha: ved.sha } })
    closeVaultEdit()
    await afterVaultWrite(r.changed ? `Saved and re-encrypted ${r.file}.` : 'Nothing changed, so the file was left as it is.')
  } catch (e) { ved.error = conflictMsg(e) } finally { ved.busy = false }
}
// open the editor for a vault file linked from the Code screen (?edit=<file>)
watch(sec, (v) => {
  const f = route.query.edit
  if (!v || !f) return
  const s = v.secrets.find((x) => x.kind === 'vault-file' && x.file === f)
  router.replace({ query: { ...route.query, edit: undefined } })
  if (s?.revealable) openVaultEdit(s)
  else if (s) revealErr.value = `${s.file}: ${s.message || s.state}`
})

type NewMode = 'variable' | 'file' | 'encrypt'
const NEW_MODES: [NewMode, string][] = [['variable', 'Encrypted variable'], ['file', 'Encrypted file'], ['encrypt', 'Encrypt an existing file']]
const nv = reactive({ open: false, mode: 'variable' as NewMode, file: '', key: '', value: '', vaultId: '', busy: false, error: '' })
const nvIds = computed(() => idsFor(nv.file))
watch(nvIds, (ids) => { if (!ids.some((i) => i.label === nv.vaultId)) nv.vaultId = ids[0]?.label ?? '' })
const openNew = () => Object.assign(nv, { open: true, mode: 'variable', file: '', key: '', value: '', error: '' })
const closeNew = () => Object.assign(nv, { open: false, value: '', error: '' })
const nvReady = computed(() => !!nv.file.trim() && (nv.mode === 'encrypt' || (nv.mode === 'variable' ? !!nv.key.trim() && nv.value !== '' : nv.value !== '')))
async function submitNew() {
  nv.busy = true; nv.error = ''
  const vault_id = nvIds.value.length > 1 ? nv.vaultId : ''
  const file = nv.file.trim().replace(/^\.\//, '')
  try {
    if (nv.mode === 'variable') {
      await api('/secrets/vault/add', { body: { file, key: nv.key.trim(), value: nv.value, vault_id } })
      closeNew(); await afterVaultWrite(`Added ${nv.key.trim()} to ${file}, encrypted.`)
    } else if (nv.mode === 'file') {
      await api('/secrets/vault/create', { body: { file, value: nv.value, vault_id } })
      closeNew(); await afterVaultWrite(`Created ${file}, encrypted.`)
    } else {
      const r = await api<{ note: string }>('/secrets/vault/encrypt', { body: { file, vault_id } })
      closeNew(); await afterVaultWrite(`Encrypted ${file}. ${r.note}`)
    }
  } catch (e) { nv.error = conflictMsg(e) } finally { nv.busy = false }
}

// ---- typed vault passwords: unlock and lock ----
// The password is sent once to the service, which keeps it in memory; it
// lives in this ref only while the dialog is open.
const typedIds = computed(() => vaultProjects.value.flatMap((p) => p.ids.filter((i) => i.prompt).map((i) => ({ project: p.path, ...i }))))
const ul = reactive({ open: false, project: '', vaultId: '', pw: '', busy: false, error: '' })
const ulChoices = computed(() => typedIds.value.filter((i) => !i.unlocked))
function openUnlock(project?: string, vaultId?: string) {
  const first = ulChoices.value.find((i) => (!project || i.project === project) && (!vaultId || i.label === vaultId)) ?? ulChoices.value[0]
  if (!first) return
  Object.assign(ul, { open: true, project: first.project, vaultId: first.label, pw: '', error: '' })
}
const closeUnlock = () => Object.assign(ul, { open: false, pw: '', error: '' })
function pickUnlock(v: string) {
  const [project, vaultId] = JSON.parse(v) as [string, string]
  Object.assign(ul, { project, vaultId })
}
async function submitUnlock() {
  ul.busy = true; ul.error = ''
  try {
    const r = await api<{ checked: boolean }>('/secrets/vault/unlock', { body: { project: ul.project, vault_id: ul.vaultId === 'default' ? '' : ul.vaultId, password: ul.pw } })
    closeUnlock()
    await afterVaultWrite(r.checked
      ? 'Unlocked. groundwork keeps the password in memory, never on disk, until you lock the vault or the service stops.'
      : 'Nothing is encrypted with this vault ID yet, so the password couldn\'t be checked. New secrets will be encrypted with it.')
  } catch (e) { ul.error = msg(e) } finally { ul.busy = false }
}
async function lockVault(project: string, vaultId: string) {
  try {
    await api('/secrets/vault/lock', { body: { project, vault_id: vaultId === 'default' ? '' : vaultId } })
    await afterVaultWrite('Locked. groundwork forgot the password.')
  } catch (e) { revealErr.value = msg(e) }
}
// the identity a locked secret waits for
function unlockFor(s: SecretItem) {
  const f = s.file ?? ''
  const mine = ulChoices.value.filter((i) => i.project === '.' || f === i.project || f.startsWith(i.project + '/'))
    .sort((a, b) => b.project.length - a.project.length) // the innermost project first
  const p = mine.find((i) => i.label === (s.vault_id || 'default')) ?? mine[0]
  openUnlock(p?.project, p?.label)
}
// ?unlock=1 (from a failed run's fix) opens the dialog once the vault IDs are known
watch(sec, () => {
  if (route.query.unlock && ulChoices.value.length) {
    router.replace({ query: { ...route.query, unlock: undefined } })
    openUnlock()
  }
})

const rk = reactive({ open: false, project: '', vaultId: '', pw: '', pw2: '', update: true, plan: null as RekeyPlan | null, loading: false, busy: false, error: '' })
const rkIds = computed(() => vaultProjects.value.find((p) => p.path === rk.project)?.ids ?? [])
const rkBlocked = computed(() => rk.plan?.items.filter((i) => i.problem) ?? [])
const rkReady = computed(() => !!rk.plan?.items.length && !rkBlocked.value.length && rk.pw !== '' && rk.pw === rk.pw2 && (!rk.update || !!rk.plan?.password_file))
function openRekey() {
  const p = vaultProjects.value[0]
  Object.assign(rk, { open: true, project: p?.path ?? '', vaultId: p?.ids[0]?.label ?? '', pw: '', pw2: '', plan: null, error: '' })
  previewRekey()
}
const closeRekey = () => Object.assign(rk, { open: false, pw: '', pw2: '', plan: null, error: '' })
async function previewRekey() {
  if (!rk.project) return
  rk.loading = true; rk.error = ''; rk.plan = null
  try {
    rk.plan = await api<RekeyPlan>('/secrets/vault/rekey', { body: { project: rk.project, vault_id: rk.vaultId } })
    rk.update = !!rk.plan.password_file
  } catch (e) { rk.error = msg(e) } finally { rk.loading = false }
}
function pickRekeyProject(path: string) {
  rk.project = path
  rk.vaultId = rkIds.value[0]?.label ?? ''
  previewRekey()
}
async function applyRekey() {
  rk.busy = true; rk.error = ''
  try {
    const r = await api<{ files: string[]; values: number; password_file_updated: boolean; typed: boolean }>('/secrets/vault/rekey', {
      body: { project: rk.project, vault_id: rk.vaultId, password: rk.pw, update_password_file: rk.update, apply: true },
    })
    const where = r.password_file_updated ? `The new password is in ${rk.plan?.password_file}.`
      : r.typed ? 'The console now uses the new password; anyone else who types it needs the new one.'
        : 'Put the new password where ansible reads it before the next run.'
    closeRekey()
    await afterVaultWrite(`Re-encrypted ${r.values} value${r.values === 1 ? '' : 's'} in ${r.files.length} file${r.files.length === 1 ? '' : 's'}. ${where} Older commits still open with the old password.`)
  } catch (e) { rk.error = conflictMsg(e) } finally { rk.busy = false }
}
onBeforeUnmount(() => { closeVaultEdit(); closeNew(); closeRekey(); closeUnlock() })
</script>

<template>
  <AppShell>
    <header class="vhead">
      <h1>Variables</h1>
      <div class="seg-group" role="group" aria-label="Source">
        <button v-for="t in (['terraform', 'ansible', 'secrets'] as const)" :key="t" class="seg" :class="{ on: tab === t }" type="button" :aria-pressed="tab === t" @click="setTab(t)">
          {{ t[0].toUpperCase() + t.slice(1) }} · {{ counts[t] }}
        </button>
      </div>
      <label v-if="tab === 'terraform' && (tf?.stacks.length ?? 0) > 1" class="picker">Stack
        <select class="inp" aria-label="Stack" :value="tf?.stack" @change="router.replace({ query: { ...route.query, stack: ($event.target as HTMLSelectElement).value } })">
          <option v-for="s in tf?.stacks" :key="s.id" :value="s.id">{{ s.id }}</option>
        </select>
      </label>
      <label v-if="tab === 'ansible' && (ans?.projects.length ?? 0) > 1" class="picker">Project
        <select class="inp" aria-label="Project" :value="ans?.project" @change="router.replace({ query: { ...route.query, project: ($event.target as HTMLSelectElement).value } })">
          <option v-for="p in ans?.projects" :key="p" :value="p">{{ p }}</option>
        </select>
      </label>
      <label class="search">
        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><circle cx="11" cy="11" r="7" /><path d="M20 20l-4-4" /></svg>
        <input v-model="q" type="search" placeholder="Find variable" aria-label="Find variable" />
      </label>
    </header>
    <p v-if="error" class="pad err" role="alert">{{ error }}</p>

    <div class="cols">
      <div class="body">
        <!-- Terraform -->
        <template v-if="tab === 'terraform'">
          <div class="legend small muted">
            <span><span class="mono c-set">value</span> set in tfvars</span>
            <span><span class="mono c-def">default</span> from variables.tf</span>
            <span><span class="mono c-sec">••••</span> secret</span>
            <span><span class="mono c-miss">missing</span> required, no value</span>
            <label class="chk"><input v-model="onlyDiffer" type="checkbox" />Only rows that differ</label>
          </div>
          <p v-if="tf && !tf.stacks.length" class="muted">No Terraform roots are configured.</p>
          <section v-else-if="tf" class="matrix" aria-label="Variables by environment">
            <table class="mono vt">
              <thead><tr><th>variable</th><th v-for="t in tfEnvs" :key="t.root + t.env">{{ t.env }}</th></tr></thead>
              <tbody>
                <tr v-for="r in tfRows" :key="r.name" :class="{ sel: r.name === selected }">
                  <th scope="row"><button type="button" class="vname" @click="select(r)">{{ r.name }}</button><small>{{ r.decl?.type || 'any' }}<template v-if="r.decl && !r.decl.has_default"> · required</template><template v-if="r.unused"> · unused</template></small></th>
                  <td v-for="c in r.cells" :key="c.env" :class="{ cur: r.name === selected && c.env === selEnv }">
                    <button type="button" class="cell" :aria-label="`${r.name} in ${c.env}: ${cellText(c)}`" @click="select(r, c.env)">
                      <span :class="CELL_CLASS[c.state]">{{ cellText(c) }}</span><small>{{ c.state === 'missing' ? 'required' : c.source }}</small>
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
            <p v-if="!tfRows.length" class="pad muted">No variables{{ q || onlyDiffer ? ' match' : '' }}.</p>
          </section>
        </template>

        <!-- Ansible -->
        <template v-else-if="tab === 'ansible'">
          <div class="legend small muted">
            <span>Group variables per environment. Host variables are on the <RouterLink to="/inventory">Inventory</RouterLink> screen.</span>
            <label class="chk"><input v-model="onlyDiffer" type="checkbox" />Only rows that differ</label>
          </div>
          <p v-if="ans && !ans.projects.length" class="muted">No Ansible projects are configured.</p>
          <section v-else-if="ans?.matrix" class="matrix" aria-label="Group variables by environment">
            <table class="mono vt">
              <thead><tr><th>group · variable</th><th v-for="e in ans.matrix.envs" :key="e">{{ e }}</th></tr></thead>
              <tbody>
                <tr v-for="r in ansRows" :key="r.group + r.name">
                  <th scope="row">{{ ansRowLabel(r) }}<small v-if="r.differs">differs</small></th>
                  <td v-for="e in ans.matrix.envs" :key="e">
                    <template v-if="r.cells[e].state === 'absent'"><span class="c-abs">—</span></template>
                    <template v-else>
                      <span :class="CELL_CLASS[r.cells[e].state]">{{ r.cells[e].value }}</span>
                      <RouterLink v-if="r.cells[e].file" class="srcl" :to="codeHref(r.cells[e].file!, r.cells[e].line)">{{ r.cells[e].source }}</RouterLink>
                    </template>
                  </td>
                </tr>
              </tbody>
            </table>
            <p v-if="!ansRows.length" class="pad muted">No group variables{{ q || onlyDiffer ? ' match' : '' }}.</p>
            <template v-for="(fs, env) in ans.matrix.encrypted_files ?? {}" :key="env">
              <p v-for="f in fs" :key="f" class="small muted pad2">🔒 <span class="mono">{{ f }}</span> ({{ env }}) is vault-encrypted; its keys are listed under Secrets.</p>
            </template>
          </section>
        </template>

        <!-- Secrets -->
        <section v-if="tab === 'secrets' || findings?.length" class="secrets" aria-label="Secrets">
          <div class="shead">
            <h2>Secrets</h2>
            <span class="small muted">decrypted only in memory, only when you ask</span>
            <span class="sact">
              <template v-if="tab === 'secrets' && vaultProjects.length">
                <button class="btn sm" type="button" @click="openNew">New secret</button>
                <button class="btn sm" type="button" @click="openRekey">Rekey…</button>
              </template>
              <button class="btn sm" type="button" :disabled="scanning" @click="scan">{{ scanning ? 'Scanning…' : 'Scan for plaintext secrets' }}</button>
            </span>
          </div>
          <template v-if="tab === 'secrets'">
            <div v-for="i in typedIds" :key="i.project + i.label" class="vlock" :class="{ on: i.unlocked }">
              <span class="small">{{ i.unlocked ? 'Vault unlocked' : 'Vault locked' }}<template v-if="vaultProjects.length > 1"> · <span class="mono">{{ i.project }}</span></template><template v-if="i.label !== 'default'"> · vault ID <span class="mono">{{ i.label }}</span></template></span>
              <span class="small muted">{{ i.unlocked ? 'the password is in memory until you lock it or the service stops' : 'no password file: type the password to use the vault in the console and in runs' }}</span>
              <button v-if="i.unlocked" class="btn xs" type="button" @click="lockVault(i.project, i.label)">Lock</button>
              <button v-else class="btn xs" type="button" @click="openUnlock(i.project, i.label)">Unlock…</button>
            </div>
            <p v-if="secLoading && !sec" class="pad muted">Checking which secrets can be decrypted…</p>
            <div v-for="s in secrets" :key="s.id" class="srow">
              <span class="mono">{{ s.name }}</span>
              <span class="where"><span>{{ s.store }}<template v-if="s.vault_id"> · vault ID <span class="mono">{{ s.vault_id }}</span></template></span><RouterLink v-if="s.file" class="mono small" :to="codeHref(s.file, s.line)">{{ s.file }}<template v-if="s.line && s.line > 1">:{{ s.line }}</template></RouterLink></span>
              <span class="pill" :class="(STATE[s.state] ?? ['st-idle'])[0]" :title="s.message">{{ (STATE[s.state] ?? ['', s.state])[1] }}</span>
              <span class="act">
                <button v-if="s.revealable && reveal?.key !== s.id" class="btn xs" type="button" @click="revealSecret(s)">Reveal</button>
                <button v-else-if="reveal?.key === s.id" class="btn xs" type="button" @click="reveal = null">Hide</button>
                <button v-else-if="s.state === 'locked'" class="btn xs" type="button" @click="unlockFor(s)">Unlock…</button>
                <span v-else class="small muted">{{ s.message }}</span>
                <button v-if="s.revealable && isVault(s)" class="btn xs" type="button" @click="openVaultEdit(s)">Edit</button>
              </span>
              <pre v-if="reveal?.key === s.id" class="revealed" aria-live="polite">{{ reveal.value }}</pre>
            </div>
            <p v-if="sec && !secrets.length" class="pad muted">No encrypted values or credential variables found.</p>
            <p v-if="revealErr" class="pad2 err" role="alert">{{ revealErr }}</p>
          </template>
          <div v-for="f in findings ?? []" :key="f.file + f.line" class="finding">
            <span class="pill st-fail">plaintext</span>
            <RouterLink class="mono small" :to="codeHref(f.file, f.line)">{{ f.file }}:{{ f.line }}</RouterLink>
            <span class="small fmsg">{{ f.message }}</span>
            <button v-if="canMove(f)" class="btn xs" type="button" @click="previewMove(f)">Move to vault</button>
            <span v-else class="small muted" title="See docs/variables.md">mark it sensitive and pass it with TF_VAR_{{ f.key || 'name' }}</span>
          </div>
          <p v-if="tab === 'secrets' && findings && !findings.length" class="pad2 small ok">No plaintext secrets found.</p>
          <p v-if="move.note" class="pad2 small warn" role="status">Moved to ansible-vault. {{ move.note }}</p>
          <p v-if="vaultNote" class="pad2 small ok" role="status">{{ vaultNote }}</p>
          <div v-if="tab === 'secrets' && sec?.reveals.length" class="audit small muted">
            <span class="lbl">Recent reveals</span>
            <span v-for="r in sec.reveals.slice(0, 5)" :key="r.id" class="mono">{{ r.name }} · {{ r.file }} · {{ new Date(r.revealed_at).toLocaleString() }}</span>
          </div>
        </section>
      </div>

      <!-- detail -->
      <aside v-if="tab === 'terraform' && row" class="detail" aria-label="Variable detail">
        <div class="col">
          <span class="lbl">Selected</span>
          <span class="mono dname">{{ row.name }}</span>
          <span v-if="missingIn.length" class="pill st-fail">missing in {{ missingIn.join(', ') }}</span>
          <span v-else-if="row.unused" class="pill st-idle">not used</span>
        </div>
        <div v-if="row.decl" class="kv mono">
          <span>type</span><span>{{ row.decl.type || 'any' }}</span>
          <span>required</span><span>{{ row.decl.has_default ? 'no' : 'yes (no default)' }}</span>
          <template v-if="row.decl.has_default"><span>default</span><span>{{ row.decl.sensitive ? '••••••' : row.decl.default }}</span></template>
          <template v-for="(v, i) in row.decl.validation ?? []" :key="i"><span>validation</span><span>{{ v }}</span></template>
          <span>declared</span><RouterLink :to="codeHref(row.decl.declared.file, row.decl.declared.line)">{{ row.decl.declared.file.split('/').pop() }}:{{ row.decl.declared.line }}</RouterLink>
          <template v-if="row.decl.used_in?.length"><span>used in</span><span class="uses"><RouterLink v-for="u in row.decl.used_in.slice(0, 6)" :key="u.file + u.line" :to="codeHref(u.file, u.line)">{{ u.file.split('/').pop() }}:{{ u.line }}</RouterLink></span></template>
        </div>
        <p v-if="row.decl?.description" class="desc">"{{ row.decl.description }}"</p>
        <div class="envpick" role="group" aria-label="Environment">
          <button v-for="c in row.cells" :key="c.env" class="seg" :class="{ on: c.env === selEnv }" type="button" @click="selEnv = c.env">{{ c.env }}</button>
        </div>
        <div v-if="cell" class="editor">
          <span class="small muted">{{ cell.env }}: <span class="mono">{{ cell.state === 'missing' ? 'no value' : cell.source }}</span><template v-if="cell.file"> · <RouterLink :to="codeHref(cell.file, cell.line)" class="mono">{{ cell.file }}:{{ cell.line }}</RouterLink></template></span>
          <template v-if="row.sensitive">
            <p class="small muted">Sensitive: groundwork won't write it to a tfvars file. Set <span class="mono">TF_VAR_{{ row.name }}</span> in the environment, or keep it in SOPS.</p>
            <button v-if="cell.state === 'secret'" class="btn" type="button" @click="reveal?.key === 'tf:' + row.name + cell.env ? (reveal = null) : revealTF()">{{ reveal?.key === 'tf:' + row.name + cell.env ? 'Hide' : 'Reveal' }}</button>
            <pre v-if="reveal?.key === 'tf:' + row.name + cell.env" class="revealed">{{ reveal.value }}</pre>
            <p v-if="revealErr" class="err">{{ revealErr }}</p>
          </template>
          <form v-else-if="cell.state !== 'absent'" class="col" @submit.prevent="edit.diff ? saveEdit() : previewEdit()">
            <label class="field">Value for {{ cell.env }}
              <input v-model="edit.value" class="inp" type="text" :placeholder="row.decl?.type?.startsWith('string') || !row.decl?.type ? '&quot;text in quotes&quot;' : 'e.g. 3, true, [&quot;a&quot;]'" autocomplete="off" spellcheck="false" @input="edit.diff = ''" />
            </label>
            <label class="field">Write to
              <select v-model="edit.file" class="inp" @change="edit.diff = ''"><option v-for="f in cell.writable" :key="f" :value="f">{{ f }}</option></select>
            </label>
            <div v-if="edit.diff" class="diffbox"><DiffView :diff="edit.diff" /></div>
            <p v-if="edit.error" class="err" role="alert">{{ edit.error }}</p>
            <p v-if="edit.saved" class="small ok" role="status">{{ edit.saved }}</p>
            <button class="btn primary" type="submit" :disabled="edit.saving || !edit.value.trim()">{{ edit.diff ? (edit.create ? 'Create file and validate' : 'Save and validate') : 'Preview change' }}</button>
          </form>
          <p v-else class="small muted">Not declared in {{ cell.root }}.</p>
        </div>
      </aside>
    </div>

    <Modal v-if="ved.s" labelledby="ved-title" wide @close="closeVaultEdit">
      <form class="card dlg" @submit.prevent="saveVaultEdit">
        <h2 id="ved-title">Edit {{ ved.s.name }}</h2>
        <p class="small muted">
          <span class="mono">{{ ved.s.file }}</span><template v-if="ved.s.vault_id"> · vault ID <span class="mono">{{ ved.s.vault_id }}</span></template>.
          Saving re-encrypts it with the same vault ID; the decrypted text never touches the disk. Opening it is recorded as a reveal.
        </p>
        <p v-if="ved.loading" class="small muted">Decrypting…</p>
        <textarea v-else v-model="ved.value" class="inp mono vtext" :rows="ved.s.kind === 'vault-file' ? 14 : 4" aria-label="Decrypted value" spellcheck="false" autocomplete="off" />
        <p v-if="ved.error" class="err" role="alert">{{ ved.error }}</p>
        <div class="dact">
          <button class="btn" type="button" @click="closeVaultEdit">Cancel</button>
          <button class="btn primary" type="submit" :disabled="ved.loading || ved.busy || !ved.sha">{{ ved.busy ? 'Encrypting…' : 'Save encrypted' }}</button>
        </div>
      </form>
    </Modal>

    <Modal v-if="nv.open" labelledby="nv-title" wide @close="closeNew">
      <form class="card dlg" @submit.prevent="submitNew">
        <h2 id="nv-title">New secret</h2>
        <div class="seg-group" role="group" aria-label="Kind">
          <button v-for="[m, label] in NEW_MODES" :key="m" class="seg" :class="{ on: nv.mode === m }" type="button" :aria-pressed="nv.mode === m" @click="nv.mode = m; nv.error = ''">{{ label }}</button>
        </div>
        <p class="small muted">
          <template v-if="nv.mode === 'variable'">Adds <span class="mono">name: !vault |</span> to a YAML vars file, creating the file if needed.</template>
          <template v-else-if="nv.mode === 'file'">Writes a new file encrypted as a whole, like <span class="mono">ansible-vault create</span>.</template>
          <template v-else>Encrypts a plaintext file in place. If it was committed, the plaintext stays in git history: rotate what it holds.</template>
        </p>
        <label class="field">File, from the repository root
          <input v-model="nv.file" class="inp mono" type="text" :placeholder="nv.mode === 'variable' ? 'group_vars/prod/main.yml' : 'group_vars/prod/vault.yml'" autocomplete="off" spellcheck="false" />
        </label>
        <label v-if="nv.mode === 'variable'" class="field">Variable name
          <input v-model="nv.key" class="inp mono" type="text" placeholder="db_password" autocomplete="off" spellcheck="false" />
        </label>
        <label v-if="nv.mode !== 'encrypt'" class="field">{{ nv.mode === 'variable' ? 'Value' : 'Content' }}
          <textarea v-model="nv.value" class="inp mono vtext" :rows="nv.mode === 'variable' ? 3 : 10" spellcheck="false" autocomplete="off" />
        </label>
        <label v-if="nvIds.length > 1" class="field">Vault ID
          <select v-model="nv.vaultId" class="inp"><option v-for="i in nvIds" :key="i.label" :value="i.label">{{ i.label }}</option></select>
        </label>
        <p v-if="nv.error" class="err" role="alert">{{ nv.error }}</p>
        <div class="dact">
          <button class="btn" type="button" @click="closeNew">Cancel</button>
          <button class="btn primary" type="submit" :disabled="nv.busy || !nvReady">{{ nv.busy ? 'Encrypting…' : 'Encrypt and save' }}</button>
        </div>
      </form>
    </Modal>

    <Modal v-if="rk.open" labelledby="rk-title" wide @close="closeRekey">
      <form class="card dlg" @submit.prevent="applyRekey">
        <h2 id="rk-title">Rekey ansible-vault</h2>
        <p class="small muted">Re-encrypts every value below with a new password. All files are written, or none are.</p>
        <div v-if="vaultProjects.length > 1 || rkIds.length > 1" class="rkpick">
          <label v-if="vaultProjects.length > 1" class="field">Project
            <select class="inp" :value="rk.project" @change="pickRekeyProject(($event.target as HTMLSelectElement).value)"><option v-for="p in vaultProjects" :key="p.path" :value="p.path">{{ p.path }}</option></select>
          </label>
          <label v-if="rkIds.length > 1" class="field">Vault ID
            <select v-model="rk.vaultId" class="inp" @change="previewRekey"><option v-for="i in rkIds" :key="i.label" :value="i.label">{{ i.label }}</option></select>
          </label>
        </div>
        <p v-if="rk.loading" class="small muted">Checking what can be decrypted…</p>
        <ul v-else-if="rk.plan?.items.length" class="rklist mono small">
          <li v-for="i in rk.plan.items" :key="i.id" :class="{ err: i.problem }">{{ i.file }}<template v-if="i.kind === 'vault-inline'"> · {{ i.name }}</template><template v-if="i.problem"> · {{ i.problem }}</template></li>
        </ul>
        <p v-else-if="rk.plan" class="small muted">Nothing here is encrypted with this vault ID.</p>
        <p v-if="rkBlocked.length" class="err small">Fix the values the current password can't open first.</p>
        <template v-if="rk.plan?.items.length && !rkBlocked.length">
          <label class="field">New password
            <input v-model="rk.pw" class="inp" type="password" autocomplete="new-password" />
          </label>
          <label class="field">Repeat it
            <input v-model="rk.pw2" class="inp" type="password" autocomplete="new-password" />
          </label>
          <p v-if="rk.pw2 && rk.pw !== rk.pw2" class="err small">The passwords don't match.</p>
          <label v-if="rk.plan.password_file" class="chk2"><input v-model="rk.update" type="checkbox" />Write the new password to <span class="mono">{{ rk.plan.password_file }}</span></label>
          <p v-else-if="rk.plan.typed" class="small muted">The console switches to the new password. Anyone else who types it needs the new one.</p>
          <p v-else class="small warn">groundwork can't update the password ({{ rk.plan.no_update }}). Put the new one where ansible reads it right after.</p>
        </template>
        <p v-if="rk.error" class="err" role="alert">{{ rk.error }}</p>
        <div class="dact">
          <button class="btn" type="button" @click="closeRekey">Cancel</button>
          <button class="btn primary" type="submit" :disabled="rk.busy || !rkReady">{{ rk.busy ? 'Re-encrypting…' : 'Rekey' }}</button>
        </div>
      </form>
    </Modal>

    <Modal v-if="ul.open" labelledby="ul-title" @close="closeUnlock">
      <form class="card dlg" @submit.prevent="submitUnlock">
        <h2 id="ul-title">Unlock ansible-vault</h2>
        <p class="small muted">groundwork checks the password against a value it encrypts, then keeps it in memory, never on disk, until you lock the vault or the service stops. Runs, reveals and edits use it.</p>
        <label v-if="ulChoices.length > 1" class="field">Vault
          <select class="inp" :value="JSON.stringify([ul.project, ul.vaultId])" @change="pickUnlock(($event.target as HTMLSelectElement).value)">
            <option v-for="i in ulChoices" :key="i.project + i.label" :value="JSON.stringify([i.project, i.label])">{{ i.project }} · {{ i.label }}</option>
          </select>
        </label>
        <label class="field">Vault password
          <input v-model="ul.pw" class="inp" type="password" autocomplete="off" spellcheck="false" autofocus />
        </label>
        <p v-if="ul.error" class="err" role="alert">{{ ul.error }}</p>
        <div class="dact">
          <button class="btn" type="button" @click="closeUnlock">Cancel</button>
          <button class="btn primary" type="submit" :disabled="ul.busy || !ul.pw">{{ ul.busy ? 'Checking…' : 'Unlock' }}</button>
        </div>
      </form>
    </Modal>

    <Modal v-if="move.f" labelledby="mv-title" wide @close="move.f = null">
      <div class="card dlg">
        <h2 id="mv-title">Move to ansible-vault</h2>
        <p class="small muted">Encrypts <span class="mono">{{ move.f.key }}</span> in place with the project's vault password. The old value stays in git history: rotate it afterwards.</p>
        <div v-if="move.diff" class="diffbox"><DiffView :diff="move.diff" /></div>
        <p v-if="move.error" class="err" role="alert">{{ move.error }}</p>
        <div class="dact">
          <button class="btn" type="button" @click="move.f = null">Cancel</button>
          <button class="btn primary" type="button" :disabled="!move.diff || move.busy" @click="applyMove">{{ move.busy ? 'Encrypting…' : 'Encrypt' }}</button>
        </div>
      </div>
    </Modal>
  </AppShell>
</template>

<style scoped>
.vhead { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 14px 24px; border-bottom: 1px solid var(--line); }
h1 { margin: 0; font-size: 18px; font-weight: 600; }
h2 { margin: 0; font-size: 14px; font-weight: 600; }
.small { font-size: 13px; }
.pad { padding: 16px 18px; margin: 0; } .pad2 { padding: 8px 18px; margin: 0; }
.ok { color: var(--ok); } .warn { color: var(--warn); }
.seg-group, .envpick { display: flex; gap: 2px; padding: 2px; border: 1px solid var(--line-strong); border-radius: 7px; background: var(--bg-card); align-self: flex-start; }
.seg { font-size: 14px; height: 30px; padding: 0 12px; border: 0; border-radius: 5px; background: transparent; color: var(--text-muted); cursor: pointer; font-family: inherit; }
.seg.on { background: var(--line-strong); color: var(--text); }
.picker { display: flex; align-items: center; gap: 8px; font-size: 14px; color: var(--text-muted); }
.picker .inp { height: 32px; }
.search { flex: 1 1 200px; max-width: 320px; margin-left: auto; display: flex; align-items: center; gap: 8px; height: 34px; padding: 0 10px; border: 1px solid var(--line-strong); border-radius: 6px; color: var(--text-label); }
.search input { flex: 1; min-width: 0; background: transparent; border: 0; outline: none; color: var(--text); font: inherit; font-size: 14px; }
.search:focus-within { border-color: var(--accent); }
/* one row on desktop, so each column has a definite height and scrolls inside it */
.cols { display: flex; flex: 1; min-height: 0; }
.body { flex: 999 1 600px; min-width: 0; display: flex; flex-direction: column; gap: 20px; padding: 20px 24px 28px; overflow: auto; }
.body > * { flex-shrink: 0; } /* the column scrolls as one, not the table inside it */
.legend { display: flex; flex-wrap: wrap; gap: 16px; align-items: center; }
.chk { margin-left: auto; display: flex; align-items: center; gap: 6px; }
.chk input { accent-color: var(--accent); }
.matrix, .secrets { border: 1px solid var(--line); border-radius: 10px; background: var(--bg-panel); overflow-x: auto; }
.vt { width: 100%; min-width: 640px; border-collapse: collapse; font-size: 13.5px; }
.vt th, .vt td { padding: 10px 14px; text-align: left; vertical-align: top; border-top: 1px solid var(--bg-hover); font-weight: 400; }
.vt thead th { border-top: 0; color: var(--text-label); font-size: 13px; }
.vt tbody th { color: var(--text); width: 26%; }
.vt td { cursor: pointer; overflow-wrap: anywhere; }
.vt td:hover { background: var(--bg-raised); }
.vt small { display: block; margin-top: 2px; font-size: 13px; color: var(--text-label); }
.vt tr.sel { background: var(--accent-bg); } .vt tr.sel th { box-shadow: inset 3px 0 0 var(--accent); }
.vt td.cur { outline: 1px solid var(--accent); outline-offset: -1px; }
.vname { all: unset; cursor: pointer; } .vname:focus-visible { outline: 2px solid var(--accent); }
.c-set { color: var(--text); } .c-def { color: var(--text-muted); font-style: italic; } .c-sec { color: var(--warn); }
.c-miss { color: var(--fail); font-weight: 500; } .c-abs { color: var(--text-faint); }
.srcl { display: block; font-size: 13px; color: var(--text-label); text-decoration: none; margin-top: 2px; }
.srcl:hover { color: var(--accent); }
.shead { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 14px 18px; border-bottom: 1px solid var(--line); }
.sact { margin-left: auto; display: flex; flex-wrap: wrap; gap: 8px; }
.vtext { width: 100%; box-sizing: border-box; resize: vertical; font-size: 13.5px; line-height: 1.5; padding: 8px 10px; }
.rkpick { display: flex; flex-wrap: wrap; gap: 12px; }
.rklist { margin: 0; padding: 8px 12px 8px 28px; max-height: 180px; overflow: auto; border: 1px solid var(--line); border-radius: 6px; background: var(--bg-inset); }
.chk2 { display: flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text-soft); flex-wrap: wrap; }
.chk2 input { accent-color: var(--accent); }
.srow { display: grid; grid-template-columns: minmax(130px, 1fr) minmax(200px, 1.6fr) 140px minmax(120px, auto); gap: 12px; align-items: center; padding: 11px 18px; border-top: 1px solid var(--bg-hover); font-size: 13.5px; min-width: 640px; }
.where { display: flex; flex-direction: column; gap: 2px; }
.where a { color: var(--text-label); text-decoration: none; }
.act { justify-self: end; display: flex; flex-wrap: wrap; justify-content: flex-end; align-items: center; gap: 6px; text-align: right; }
.pill { font: 600 12.5px var(--font-mono); padding: 3px 8px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; justify-self: start; white-space: nowrap; align-self: flex-start; }
.st-ok { color: var(--ok); background: color-mix(in srgb, var(--ok) 10%, transparent); } .st-fail { color: var(--fail); background: color-mix(in srgb, var(--fail) 10%, transparent); }
.st-warn { color: var(--warn); background: color-mix(in srgb, var(--warn) 10%, transparent); } .st-idle { color: var(--text-muted); background: color-mix(in srgb, var(--text-muted) 10%, transparent); }
.btn.xs { height: 28px; font-size: 13px; padding: 0 10px; }
.revealed { grid-column: 1 / -1; margin: 0; padding: 10px 12px; background: var(--bg-inset); border: 1px solid var(--warn-line); border-radius: 6px; color: var(--warn); white-space: pre-wrap; overflow-wrap: anywhere; font-size: 13.5px; }
.finding { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 12px 18px; border-top: 1px solid var(--fail-line); background: var(--fail-bg); }
.finding a { color: var(--text); text-decoration: none; }
.fmsg { color: var(--text-soft); flex: 1 1 240px; }
.vlock { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 12px; padding: 10px 18px; border-top: 1px solid var(--warn-line); background: color-mix(in srgb, var(--warn) 6%, transparent); }
.vlock.on { border-top-color: var(--bg-hover); background: transparent; }
.vlock .btn { margin-left: auto; }
.audit { display: flex; flex-direction: column; gap: 4px; padding: 12px 18px; border-top: 1px solid var(--line); }
.detail { flex: 1 1 300px; max-width: 100%; box-sizing: border-box; border-left: 1px solid var(--line); padding: 20px; display: flex; flex-direction: column; gap: 18px; background: var(--bg-sunken); overflow: auto; }
.col { display: flex; flex-direction: column; gap: 6px; }
.dname { font-size: 16px; font-weight: 700; overflow-wrap: anywhere; }
.kv { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 6px 14px; font-size: 13px; color: var(--text-muted); }
.kv > span:nth-child(even) { color: var(--text); overflow-wrap: anywhere; }
.kv a { color: var(--accent); text-decoration: none; }
.uses { display: flex; flex-wrap: wrap; gap: 4px 10px; }
.desc { margin: 0; font-size: 14px; color: var(--text-soft); line-height: 1.5; }
.editor { display: flex; flex-direction: column; gap: 10px; padding: 14px; border: 1px solid var(--line); border-radius: 10px; background: var(--bg-panel); }
.editor a { color: var(--text-muted); }
.field { display: flex; flex-direction: column; gap: 6px; font-size: 13px; color: var(--text-muted); }
.diffbox { max-height: 220px; overflow: auto; border: 1px solid var(--line); border-radius: 6px; }
.dlg { width: min(560px, 100%); display: flex; flex-direction: column; gap: 12px; }
.dact { display: flex; justify-content: flex-end; gap: 8px; }
@media (max-width: 900px) { .cols { flex-wrap: wrap; } .detail { border-left: 0; border-top: 1px solid var(--line); } }
.cell { all: unset; display: flex; flex-direction: column; width: 100%; box-sizing: border-box; cursor: pointer; text-align: left; }
.cell:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
</style>
