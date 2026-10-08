<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from '../components/AppShell.vue'
import DiffView from '../components/DiffView.vue'
import { api, ApiError } from '../api/client'
import type { AnsibleVarsResponse, MatrixRow, SecretFinding, SecretItem, SecretsResponse, TerraformVarsResponse, VarCell, VarRow } from '../api/types'

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
  'can-decrypt': ['st-ok', 'can decrypt'], 'wrong-password': ['st-fail', 'wrong password'], 'no-password': ['st-warn', 'no password'],
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
                  <td v-for="c in r.cells" :key="c.env" :class="{ cur: r.name === selected && c.env === selEnv }" @click="select(r, c.env)">
                    <span :class="CELL_CLASS[c.state]">{{ cellText(c) }}</span><small>{{ c.state === 'missing' ? 'required' : c.source }}</small>
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
            <button class="btn sm" type="button" :disabled="scanning" @click="scan">{{ scanning ? 'Scanning…' : 'Scan for plaintext secrets' }}</button>
          </div>
          <template v-if="tab === 'secrets'">
            <p v-if="secLoading && !sec" class="pad muted">Checking which secrets can be decrypted…</p>
            <div v-for="s in secrets" :key="s.id" class="srow">
              <span class="mono">{{ s.name }}</span>
              <span class="where"><span>{{ s.store }}</span><RouterLink v-if="s.file" class="mono small" :to="codeHref(s.file, s.line)">{{ s.file }}<template v-if="s.line && s.line > 1">:{{ s.line }}</template></RouterLink></span>
              <span class="pill" :class="(STATE[s.state] ?? ['st-idle'])[0]" :title="s.message">{{ (STATE[s.state] ?? ['', s.state])[1] }}</span>
              <span class="act">
                <button v-if="s.revealable && reveal?.key !== s.id" class="btn xs" type="button" @click="revealSecret(s)">Reveal</button>
                <button v-else-if="reveal?.key === s.id" class="btn xs" type="button" @click="reveal = null">Hide</button>
                <span v-else class="small muted">{{ s.message }}</span>
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

    <div v-if="move.f" class="modal" role="dialog" aria-modal="true" aria-labelledby="mv-title" @keydown.esc="move.f = null">
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
    </div>
  </AppShell>
</template>

<style scoped>
.vhead { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 14px 24px; border-bottom: 1px solid var(--line); }
h1 { margin: 0; font-size: 18px; font-weight: 600; }
h2 { margin: 0; font-size: 14px; font-weight: 600; }
.small { font-size: 13px; }
.pad { padding: 16px 18px; margin: 0; } .pad2 { padding: 8px 18px; margin: 0; }
.ok { color: var(--ok); } .warn { color: var(--warn); }
.seg-group, .envpick { display: flex; gap: 2px; padding: 2px; border: 1px solid var(--line-strong); border-radius: 7px; background: #111518; align-self: flex-start; }
.seg { font-size: 14px; height: 30px; padding: 0 12px; border: 0; border-radius: 5px; background: transparent; color: var(--text-muted); cursor: pointer; font-family: inherit; }
.seg.on { background: var(--line-strong); color: var(--text); }
.picker { display: flex; align-items: center; gap: 8px; font-size: 14px; color: var(--text-muted); }
.picker .inp { height: 32px; }
.search { flex: 1 1 200px; max-width: 320px; margin-left: auto; display: flex; align-items: center; gap: 8px; height: 34px; padding: 0 10px; border: 1px solid var(--line-strong); border-radius: 6px; color: var(--text-label); }
.search input { flex: 1; min-width: 0; background: transparent; border: 0; outline: none; color: var(--text); font: inherit; font-size: 14px; }
.search:focus-within { border-color: var(--accent); }
.cols { display: flex; flex-wrap: wrap; flex: 1; min-height: 0; }
.body { flex: 999 1 600px; min-width: 0; display: flex; flex-direction: column; gap: 20px; padding: 20px 24px 28px; }
.legend { display: flex; flex-wrap: wrap; gap: 16px; align-items: center; }
.chk { margin-left: auto; display: flex; align-items: center; gap: 6px; }
.chk input { accent-color: var(--accent); }
.matrix, .secrets { border: 1px solid var(--line); border-radius: 10px; background: var(--bg-panel); overflow-x: auto; }
.vt { width: 100%; min-width: 640px; border-collapse: collapse; font-size: 13.5px; }
.vt th, .vt td { padding: 10px 14px; text-align: left; vertical-align: top; border-top: 1px solid #1A1F23; font-weight: 400; }
.vt thead th { border-top: 0; color: var(--text-label); font-size: 13px; }
.vt tbody th { color: var(--text); width: 26%; }
.vt td { cursor: pointer; overflow-wrap: anywhere; }
.vt td:hover { background: #151A1E; }
.vt small { display: block; margin-top: 2px; font-size: 12px; color: var(--text-label); }
.vt tr.sel { background: #161D12; } .vt tr.sel th { box-shadow: inset 3px 0 0 var(--accent); }
.vt td.cur { outline: 1px solid var(--accent); outline-offset: -1px; }
.vname { all: unset; cursor: pointer; } .vname:focus-visible { outline: 2px solid var(--accent); }
.c-set { color: var(--text); } .c-def { color: var(--text-muted); font-style: italic; } .c-sec { color: var(--warn); }
.c-miss { color: var(--fail); font-weight: 500; } .c-abs { color: #4E5A61; }
.srcl { display: block; font-size: 12px; color: var(--text-label); text-decoration: none; margin-top: 2px; }
.srcl:hover { color: var(--accent); }
.shead { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 14px 18px; border-bottom: 1px solid var(--line); }
.shead .btn { margin-left: auto; }
.srow { display: grid; grid-template-columns: minmax(130px, 1fr) minmax(200px, 1.6fr) 140px minmax(120px, auto); gap: 12px; align-items: center; padding: 11px 18px; border-top: 1px solid #1A1F23; font-size: 13.5px; min-width: 640px; }
.where { display: flex; flex-direction: column; gap: 2px; }
.where a { color: var(--text-label); text-decoration: none; }
.act { justify-self: end; text-align: right; }
.pill { font: 500 12px var(--font-mono); padding: 3px 8px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; justify-self: start; white-space: nowrap; align-self: flex-start; }
.st-ok { color: var(--ok); background: rgba(95, 211, 141, .10); } .st-fail { color: var(--fail); background: rgba(255, 122, 122, .10); }
.st-warn { color: var(--warn); background: rgba(245, 182, 71, .10); } .st-idle { color: var(--text-muted); background: rgba(154, 164, 171, .10); }
.btn.xs { height: 28px; font-size: 13px; padding: 0 10px; }
.revealed { grid-column: 1 / -1; margin: 0; padding: 10px 12px; background: var(--bg-inset, #0A0C0E); border: 1px solid #4A3B1A; border-radius: 6px; color: var(--warn); white-space: pre-wrap; overflow-wrap: anywhere; font-size: 13.5px; }
.finding { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 12px 18px; border-top: 1px solid #3A2626; background: #161011; }
.finding a { color: var(--text); text-decoration: none; }
.fmsg { color: #C8CFD4; flex: 1 1 240px; }
.audit { display: flex; flex-direction: column; gap: 4px; padding: 12px 18px; border-top: 1px solid var(--line); }
.detail { flex: 1 1 300px; max-width: 100%; box-sizing: border-box; border-left: 1px solid var(--line); padding: 20px; display: flex; flex-direction: column; gap: 18px; background: #0F1315; }
.col { display: flex; flex-direction: column; gap: 6px; }
.dname { font-size: 16px; font-weight: 700; overflow-wrap: anywhere; }
.kv { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 6px 14px; font-size: 13px; color: var(--text-muted); }
.kv > span:nth-child(even) { color: var(--text); overflow-wrap: anywhere; }
.kv a { color: var(--accent); text-decoration: none; }
.uses { display: flex; flex-wrap: wrap; gap: 4px 10px; }
.desc { margin: 0; font-size: 14px; color: #C8CFD4; line-height: 1.5; }
.editor { display: flex; flex-direction: column; gap: 10px; padding: 14px; border: 1px solid var(--line); border-radius: 10px; background: var(--bg-panel); }
.editor a { color: var(--text-muted); }
.field { display: flex; flex-direction: column; gap: 6px; font-size: 13px; color: var(--text-muted); }
.diffbox { max-height: 220px; overflow: auto; border: 1px solid var(--line); border-radius: 6px; }
.modal { position: fixed; inset: 0; background: rgba(0, 0, 0, .55); display: grid; place-items: center; z-index: 50; padding: 16px; }
.dlg { width: min(560px, 100%); display: flex; flex-direction: column; gap: 12px; }
.dact { display: flex; justify-content: flex-end; gap: 8px; }
</style>
