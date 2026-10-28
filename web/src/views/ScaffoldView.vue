<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import SetupShell from '../components/SetupShell.vue'
import { api, apiBlob, ApiError, fieldErrors } from '../api/client'
import type { PlannedFile, PreviewResponse, ScaffoldForm, SetupTool, WriteResult } from '../api/types'
import { useWorkspace } from '../stores/workspace'

const router = useRouter()
const store = useWorkspace()

const form = reactive<ScaffoldForm>({
  project: '', tools: ['terraform', 'ansible'], envs: ['dev', 'staging', 'prod'], layout: 'dirs', vault_password_file: '',
  checks: ['fmt', 'validate', 'tflint', 'ansible-lint', 'yamllint'],
})
// the vault password: typed in the console (no file) or kept in a file outside the repo
const vaultPw = ref<'typed' | 'file'>('typed')
const vaultFile = ref('')
const vaultFileTouched = ref(false)
watch([vaultPw, vaultFile], ([mode, file]) => { form.vault_password_file = mode === 'file' ? file : '' }, { immediate: true })
watch(() => form.project, (p) => { if (!vaultFileTouched.value) vaultFile.value = `~/.vault-pass-${p || 'infra'}` })
const password = ref('')
const password2 = ref('')
const newEnv = ref('')
const gitCommit = ref(false)
const overwrite = reactive(new Set<string>())

const preview = ref<PreviewResponse | null>(null)
const errors = ref<Record<string, string[]>>({})
const selected = ref('')
const busy = ref(false)
const failure = ref('')
const done = ref<{ results: WriteResult[]; vault?: string; vault_file?: string; vault_error?: string; git_commit?: string; git_error?: string } | null>(null)

const envChoices = computed(() => [...new Set(['dev', 'staging', 'prod', ...form.envs])])
const err = (f: string) => errors.value[f]?.join(' ')

// the Terraform binary chosen ('' = none) and whether Ansible is
const iac = computed(() => form.tools.find((t) => t !== 'ansible') ?? '')
const ansible = computed(() => form.tools.includes('ansible'))
const tfName = computed(() => (iac.value === 'tofu' ? 'OpenTofu' : 'Terraform'))
const toolOptions: { id: SetupTool; label: string; bin: string }[] = [
  { id: 'terraform', label: 'Terraform', bin: 'terraform' },
  { id: 'tofu', label: 'OpenTofu', bin: 'tofu' },
  { id: 'ansible', label: 'Ansible', bin: 'ansible-playbook' },
]
// Terraform and OpenTofu exclude each other: ticking one unticks the other
function toggleTool(id: SetupTool, on: boolean) {
  const rest = form.tools.filter((t) => t !== id && !(on && id !== 'ansible' && t !== 'ansible'))
  form.tools = on ? [...rest, id].sort((a, b) => toolOptions.findIndex((o) => o.id === a) - toolOptions.findIndex((o) => o.id === b)) : rest
}
const toolFound = (id: string) => store.ws?.tools.find((t) => t.name === id)?.found

const checkOptions = computed(() => [
  ...(iac.value ? [
    { id: 'fmt', label: `${iac.value} fmt` }, { id: 'validate', label: `${iac.value} validate` },
    { id: 'tflint', label: 'tflint' }, { id: 'checkov', label: 'checkov' },
  ] : []),
  ...(ansible.value ? [{ id: 'ansible-lint', label: 'ansible-lint' }, { id: 'yamllint', label: 'yamllint' }] : []),
])
// sections are numbered by what is shown
const step = computed(() => {
  let n = 2
  const s = { env: 0, ans: 0, chk: 0 }
  if (iac.value) s.env = ++n
  if (ansible.value) s.ans = ++n
  s.chk = n + 1
  return s
})

onMounted(async () => {
  const ws = store.ws ?? (await store.refresh())
  form.project = (ws?.name ?? 'infra').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '') || 'infra'
  if (ws?.default_tools?.length) form.tools = [...ws.default_tools]
  gitCommit.value = !!ws?.git
})

let timer: number | undefined
let seq = 0
watch(form, () => {
  window.clearTimeout(timer)
  timer = window.setTimeout(refreshPreview, 250)
}, { deep: true, immediate: true })

async function refreshPreview() {
  const mine = ++seq
  try {
    const res = await api<PreviewResponse>('/setup/scaffold/preview', { body: { form } })
    if (mine !== seq) return
    preview.value = res
    errors.value = {}
    for (const p of [...overwrite]) if (!res.files.some((f) => f.path === p && f.exists)) overwrite.delete(p)
    if (!res.files.some((f) => f.path === selected.value)) selected.value = ''
  } catch (e) {
    if (mine !== seq) return
    preview.value = null
    errors.value = fieldErrors(e)
    if (!Object.keys(errors.value).length) failure.value = e instanceof Error ? e.message : String(e)
  }
}

function addEnv() {
  const e = newEnv.value.trim().toLowerCase()
  if (e && !form.envs.includes(e)) form.envs.push(e)
  newEnv.value = ''
}
function toggleEnv(e: string, on: boolean) {
  const i = form.envs.indexOf(e)
  if (on && i < 0) form.envs.push(e)
  if (!on && i >= 0) form.envs.splice(i, 1)
}
function toggleCheck(id: string, on: boolean) {
  const i = form.checks.indexOf(id)
  if (on && i < 0) form.checks.push(id)
  if (!on && i >= 0) form.checks.splice(i, 1)
}

// the password is asked for unless an existing password file already holds it
const fileExists = computed(() => vaultPw.value === 'file' && !!preview.value?.vault_file?.exists)
const needPassword = computed(() => ansible.value && !fileExists.value)
const pwProblem = computed(() => {
  if (!password.value) return ''
  if (/[\r\n]/.test(password.value)) return 'The password must be one line.'
  if (password2.value && password2.value !== password.value) return "The passwords don't match."
  return ''
})
const pwReady = computed(() => !needPassword.value || (!!password.value && password.value === password2.value && !pwProblem.value))

interface Line { depth: number; name: string; dir: boolean; file?: PlannedFile }
const lines = computed<Line[]>(() => {
  const out: Line[] = []
  const seen = new Set<string>()
  for (const f of [...(preview.value?.files ?? [])].sort((a, b) => a.path.localeCompare(b.path))) {
    const parts = f.path.split('/')
    for (let i = 0; i < parts.length - 1; i++) {
      const key = parts.slice(0, i + 1).join('/')
      if (!seen.has(key)) { seen.add(key); out.push({ depth: i, name: parts[i] + '/', dir: true }) }
    }
    out.push({ depth: parts.length - 1, name: parts[parts.length - 1], dir: false, file: f })
  }
  return out
})
const outsideFile = computed(() => (vaultPw.value === 'file' && preview.value?.vault_file && !preview.value.vault_file.exists ? preview.value.vault_file.path : ''))
const fileCount = computed(() => (preview.value?.new ?? 0) + (outsideFile.value ? 1 : 0))
const selectedFile = computed(() => preview.value?.files.find((f) => f.path === selected.value))
const existing = computed(() => preview.value?.files.filter((f) => f.exists && !f.merge) ?? [])
const hasErrors = computed(() => Object.keys(errors.value).length > 0)

// missing tools: one line per installer ("brew install ansible-lint yamllint"), the other ways under a fold
const missingTools = computed(() => (store.ws?.tools ?? []).filter((t) => !t.found && (
  form.tools.includes(t.name as SetupTool) || checkOptions.value.some((c) => c.id === t.name && form.checks.includes(c.id)))))
const INSTALLERS = ['brew install', 'pipx install', 'uv tool install', 'sudo apt-get install']
const installLines = computed(() => {
  const out: string[] = []
  const grouped = new Map<string, string[]>()
  for (const t of missingTools.value) {
    const h = t.hint ?? ''
    const pre = INSTALLERS.find((p) => h.startsWith(p + ' ') && !h.slice(p.length + 1).includes(' '))
    if (!pre) { if (h) out.push(h); continue }
    if (!grouped.has(pre)) { grouped.set(pre, []); out.push(pre) }
    grouped.get(pre)!.push(h.slice(pre.length + 1))
  }
  return out.map((l) => (grouped.has(l) ? `${l} ${grouped.get(l)!.join(' ')}` : l))
})
const otherWays = computed(() => missingTools.value.filter((t) => t.alternatives?.length))
// the tools chosen or installed; a missing one only warns when it was chosen
const toolLine = computed(() => (store.ws?.tools ?? []).filter((t) => ['terraform', 'tofu', 'ansible', 'tflint'].includes(t.name) &&
  (t.found || form.tools.includes(t.name as SetupTool) || (t.name === 'tflint' && !!iac.value && form.checks.includes('tflint')))))

async function create() {
  busy.value = true; failure.value = ''
  try {
    done.value = await api('/setup/scaffold', {
      body: { form, overwrite: [...overwrite], git_commit: gitCommit.value, vault_password: needPassword.value ? password.value : '' },
    })
    password.value = ''; password2.value = ''
    await store.refresh()
  } catch (e) {
    failure.value = e instanceof ApiError && e.code === 'invalid_form'
      ? (fieldErrors(e).vault_password ? `Set the ansible-vault password in step ${step.value.ans} first.` : 'Fix the highlighted fields first.')
      : e instanceof Error ? e.message : String(e)
    if (e instanceof ApiError && e.code === 'invalid_form') errors.value = { ...errors.value, ...fieldErrors(e) }
  } finally { busy.value = false }
}
async function download() {
  busy.value = true; failure.value = ''
  try {
    const blob = await apiBlob('/setup/scaffold?format=zip', { form })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `${form.project}-groundwork.zip`
    a.click()
    URL.revokeObjectURL(a.href)
  } catch (e) { failure.value = e instanceof Error ? e.message : String(e) } finally { busy.value = false }
}
const tally = computed(() => {
  const n: Record<string, number> = {}
  for (const r of done.value?.results ?? []) n[r.status] = (n[r.status] ?? 0) + 1
  return n
})
const written = computed(() => (done.value?.results ?? []).filter((r) => r.status !== 'skipped').map((r) => r.path))
const firstRoot = computed(() => (form.layout === 'dirs' ? 'terraform/envs/<env>/' : 'terraform/'))
const initCmd = computed(() => {
  let c = `groundwork init --tools ${form.tools.join(',')}`
  if (form.vault_password_file && ansible.value) c += ` --vault-password-file ${form.vault_password_file}`
  return c
})
</script>

<template>
  <SetupShell title="Set up repository">
    <section v-if="done" class="card done" aria-live="polite">
      <h1>Repository set up</h1>
      <p class="pre">
        <template v-if="written.length">Wrote {{ written.join(', ') }}.</template><template v-if="tally.skipped"> Kept {{ tally.skipped }} existing.</template>
      </p>
      <p v-if="done.vault === 'unlocked'" class="ok">✓ Vault password set. groundwork holds it in memory until you lock the vault or the service stops.</p>
      <p v-else-if="done.vault === 'file_created'" class="ok">✓ Created <span class="mono">{{ done.vault_file }}</span> with the vault password, readable only by you.</p>
      <p v-else-if="done.vault === 'file_existing'" class="ok">✓ Using the vault password in <span class="mono">{{ done.vault_file }}</span>.</p>
      <p v-if="done.vault_error" class="err pre" role="alert">The files were written, but the vault couldn't be unlocked: {{ done.vault_error }}. Unlock it from Variables → Secrets.</p>
      <p v-if="done.git_commit" class="ok">Committed: {{ done.git_commit }}</p>
      <p v-if="done.git_error" class="err pre" role="alert">The files were written, but the commit failed: {{ done.git_error }}</p>
      <div class="stack next">
        <span class="lbl">Next</span>
        <ol>
          <li v-if="iac">Write your {{ tfName }} in <span class="mono">{{ firstRoot }}</span><template v-if="ansible"> and add the <span class="mono">ansible_hosts</span> output</template>.</li>
          <li v-if="ansible && !iac">Write your inventory in <span class="mono">ansible/inventory/</span>, such as <span class="mono">hosts.yml</span>.</li>
          <li v-if="ansible">Write your playbooks and roles under <span class="mono">ansible/</span>.</li>
          <li v-if="ansible">Put secrets in the vault from <b>Variables → Secrets → New secret</b>. groundwork encrypts them with the vault password.</li>
        </ol>
      </div>
      <div class="actions">
        <button class="btn primary" type="button" @click="router.push('/')">Open console</button>
        <button v-if="ansible" class="btn" type="button" @click="router.push('/variables?tab=secrets&new=1')">Add a secret</button>
      </div>
    </section>

    <template v-else>
      <div class="main">
        <div class="head">
          <h1>Set up this repo</h1>
          <p class="muted lead">
            No <span class="mono">groundwork.yaml</span> here yet. You write the code yourself; groundwork only adds its own config, and
            with Terraform or OpenTofu and Ansible, the inventory script that turns your hosts into Ansible hosts. Nothing touches your cloud.
          </p>
        </div>

        <pre class="term" tabindex="0" aria-label="Environment"><span class="muted">$ </span>groundwork
<template v-for="t in toolLine" :key="t.name"><span :class="t.found ? 'ok' : 'warn'">{{ t.found ? '✓' : '!' }}</span> {{ t.name }}{{ t.found ? ' ' + (t.version ?? '') : ' not found' }}  </template>
No groundwork.yaml in {{ store.ws?.root }} — starting setup</pre>

        <section class="card" aria-labelledby="s-proj">
          <h2 id="s-proj">1 · Project</h2>
          <label class="field">Project name
            <input v-model="form.project" class="inp" :class="{ bad: err('project') }" type="text" />
            <span v-if="err('project')" class="err">{{ err('project') }}</span>
          </label>
        </section>

        <section class="card" aria-labelledby="s-tools">
          <h2 id="s-tools">2 · Tools</h2>
          <fieldset>
            <legend class="small body">What will this repository use? Choose Terraform or OpenTofu, Ansible, or both. groundwork only sets up what you tick.</legend>
            <div class="grid g200">
              <label v-for="o in toolOptions" :key="o.id" class="opt tall" :class="{ on: form.tools.includes(o.id) }">
                <input type="checkbox" :checked="form.tools.includes(o.id)" @change="toggleTool(o.id, ($event.target as HTMLInputElement).checked)" />
                <span class="stack"><span>{{ o.label }}</span>
                  <span class="mono small" :class="toolFound(o.id) ? 'muted' : 'warn'">{{ o.bin }}{{ toolFound(o.id) ? '' : ' · not installed' }}</span></span>
              </label>
            </div>
          </fieldset>
          <span v-if="err('tools')" class="err">{{ err('tools') }}</span>
        </section>

        <section v-if="iac" class="card" aria-labelledby="s-env">
          <h2 id="s-env">{{ step.env }} · Environments</h2>
          <div class="wrap">
            <label v-for="e in envChoices" :key="e" class="opt" :class="{ on: form.envs.includes(e) }">
              <input type="checkbox" :checked="form.envs.includes(e)" @change="toggleEnv(e, ($event.target as HTMLInputElement).checked)" />{{ e }}
              <span v-if="e === 'prod'" class="small muted">requires approval</span>
            </label>
            <form class="add" @submit.prevent="addEnv">
              <input v-model="newEnv" class="inp" type="text" placeholder="add environment" aria-label="New environment name" />
              <button class="btn" type="submit">+ Add</button>
            </form>
          </div>
          <span v-if="err('envs')" class="err">{{ err('envs') }}</span>
          <fieldset>
            <legend class="lbl">Where your {{ tfName }} will live</legend>
            <div class="grid g260">
              <label class="opt tall" :class="{ on: form.layout === 'dirs' }">
                <input v-model="form.layout" type="radio" name="lay" value="dirs" />
                <span class="stack"><span>Directory per environment</span><span class="mono small muted">{{ form.envs.map((e) => 'terraform/envs/' + e).join(', ') }}</span></span>
              </label>
              <label class="opt tall" :class="{ on: form.layout === 'workspaces' }">
                <input v-model="form.layout" type="radio" name="lay" value="workspaces" />
                <span class="stack"><span>Workspaces</span><span class="mono small muted">terraform/, one workspace per env</span></span>
              </label>
            </div>
          </fieldset>
        </section>

        <section v-if="ansible" class="card" aria-labelledby="s-ans">
          <template v-if="iac">
            <h2 id="s-ans">{{ step.ans }} · Ansible inventory from {{ tfName }}</h2>
            <p class="small body">
              groundwork writes <span class="mono">ansible/inventory/tf_outputs.py</span>. It runs <span class="mono">{{ iac }} output -json ansible_hosts</span>
              in the environment's root and hands Ansible one group per key. Add this output to your {{ tfName }}, whatever provider you use:
            </p>
            <pre class="mono snippet" tabindex="0" aria-label="Example ansible_hosts output">output "ansible_hosts" {
  value = {
    web = [for s in vultr_instance.web : { name = s.label, ip = s.main_ip }]
    db  = [{ name = "db-1", ip = vultr_instance.db.internal_ip, ansible_user = "root" }]
  }
}</pre>
            <p class="small muted body">Each key is a group, <span class="mono">ip</span> becomes <span class="mono">ansible_host</span>, any other field becomes a host variable. Only <span class="mono">name</span> is required.</p>
          </template>
          <template v-else>
            <h2 id="s-ans">{{ step.ans }} · Ansible</h2>
            <p class="small body">
              groundwork writes <span class="mono">ansible/ansible.cfg</span>, which reads the inventory from <span class="mono">ansible/inventory/</span>.
              Put your hosts there yourself, such as in <span class="mono">inventory/hosts.yml</span>.
            </p>
          </template>
          <fieldset>
            <legend class="lbl">ansible-vault password</legend>
            <div class="grid g240">
              <label class="opt" :class="{ on: vaultPw === 'typed' }"><input v-model="vaultPw" type="radio" name="vaultpw" value="typed" />Type it in groundwork</label>
              <label class="opt" :class="{ on: vaultPw === 'file' }"><input v-model="vaultPw" type="radio" name="vaultpw" value="file" />Password file</label>
            </div>
            <div class="vbox">
              <p v-if="vaultPw === 'typed'" class="small body">Set the vault password now. groundwork keeps it in memory, never on disk, and asks for it again after the service restarts.</p>
              <label v-else class="field"><span>Password file, under <span class="mono">~/</span> or absolute</span>
                <input v-model="vaultFile" class="inp mono" :class="{ bad: err('vault_password_file') }" type="text" spellcheck="false" autocomplete="off" @input="vaultFileTouched = true" />
                <span v-if="err('vault_password_file')" class="err">{{ err('vault_password_file') }}</span>
                <span v-else-if="fileExists" class="small ok">This file exists. groundwork uses the password in it, so there's nothing to type.</span>
                <span v-else class="small muted">groundwork creates it with the password below on one line, readable only by you, and sets it as <span class="mono">vault_password_file</span> in groundwork.yaml.</span>
              </label>
              <div v-if="needPassword" class="grid g240">
                <label class="field">Vault password
                  <input v-model="password" class="inp" :class="{ bad: err('vault_password') }" type="password" autocomplete="new-password" />
                </label>
                <label class="field">Repeat it
                  <input v-model="password2" class="inp" :class="{ bad: !!pwProblem }" type="password" autocomplete="new-password" />
                  <span v-if="pwProblem" class="err">{{ pwProblem }}</span>
                  <span v-else-if="pwReady" class="small ok">✓ Passwords match</span>
                </label>
              </div>
            </div>
          </fieldset>
        </section>

        <section class="card" aria-labelledby="s-chk">
          <h2 id="s-chk">{{ step.chk }} · Checks</h2>
          <div class="grid g200">
            <label v-for="c in checkOptions" :key="c.id" class="check">
              <input type="checkbox" :checked="form.checks.includes(c.id)" @change="toggleCheck(c.id, ($event.target as HTMLInputElement).checked)" />{{ c.label }}
            </label>
          </div>
        </section>
      </div>

      <aside class="side" aria-label="Files to be created">
        <div class="card tight">
          <div class="between">
            <h2>Preview</h2>
            <span v-if="preview" class="mono small ok">+{{ fileCount }} files<template v-if="preview.existing"> · {{ preview.existing }} exist</template></span>
          </div>
          <p v-if="hasErrors" class="muted small">Fix the highlighted fields to see the file list.</p>
          <div v-else class="tree mono" role="group" aria-label="Files to write">
            <div v-for="l in lines" :key="(l.file?.path ?? '') + l.name + l.depth" :style="{ paddingLeft: l.depth * 14 + 'px' }">
              <span v-if="l.dir" class="muted">{{ l.name }}</span>
              <button v-else type="button" class="file" :class="{ sel: selected === l.file!.path, newf: !l.file!.exists }" @click="selected = l.file!.path">
                {{ l.name }}<span v-if="l.file!.exists" class="muted"> {{ l.file!.merge ? '(merge)' : '(exists)' }}</span>
              </button>
            </div>
            <div v-if="outsideFile" class="outside">
              <span class="muted">outside the repo:</span>
              <div><span class="ok">{{ outsideFile }}</span> <span class="muted">(600)</span></div>
            </div>
          </div>
          <div v-if="selectedFile" class="viewer">
            <span class="lbl mono">{{ selectedFile.path }}</span>
            <pre class="mono" tabindex="0" :aria-label="`Preview of ${selectedFile.path}`">{{ selectedFile.content }}</pre>
          </div>
        </div>

        <div class="card tight">
          <div v-if="existing.length" class="stack">
            <span class="lbl">Already exist — kept unless ticked</span>
            <label v-for="f in existing" :key="f.path" class="check mono small">
              <input type="checkbox" :checked="overwrite.has(f.path)" @change="($event.target as HTMLInputElement).checked ? overwrite.add(f.path) : overwrite.delete(f.path)" />overwrite {{ f.path }}
            </label>
          </div>
          <label class="check" :class="{ dimmed: !store.ws?.git }">
            <input v-model="gitCommit" type="checkbox" :disabled="!store.ws?.git" />Make an initial git commit
            <span v-if="!store.ws?.git" class="small muted">(not a git repo)</span>
          </label>
          <div v-if="missingTools.length" class="stack">
            <span class="lbl">Missing tools — install yourself</span>
            <span v-for="l in installLines" :key="l" class="mono small cmd"><span class="warn">!</span> {{ l }}</span>
            <span class="small muted">Commands use an installer found on this machine.</span>
            <details v-if="otherWays.length" class="ways">
              <summary class="small">Other ways to install</summary>
              <div class="alts">
                <div v-for="t in otherWays" :key="t.name" class="stack">
                  <span class="small strong">{{ t.name }}</span>
                  <span v-for="a in t.alternatives" :key="a" class="mono small muted cmd">{{ a }}</span>
                </div>
              </div>
            </details>
          </div>
          <p v-if="needPassword && !pwReady" class="small warn">! Set the ansible-vault password in step {{ step.ans }} to continue.</p>
          <p v-if="failure" class="err pre" role="alert">{{ failure }}</p>
          <div class="actions">
            <button class="btn primary grow" type="button" :disabled="busy || hasErrors || !preview || !pwReady" @click="create">Create files</button>
            <button class="btn" type="button" :disabled="busy || hasErrors || !preview" @click="download">Download as zip</button>
          </div>
          <span class="muted small">Same as <span class="mono">{{ initCmd }}</span></span>
        </div>
      </aside>
    </template>
  </SetupShell>
</template>

<style scoped>
.main { flex: 999 1 560px; min-width: 0; display: flex; flex-direction: column; gap: 20px; }
.side { flex: 1 1 340px; max-width: 100%; display: flex; flex-direction: column; gap: 14px; position: sticky; top: 24px; }
/* the side column shrinks below its content's width: long preview lines scroll in place */
.side { min-width: 0; }
/* wide screens: the side column fits the window, the file list takes what's left and Create files stays in view */
@media (min-width: 960px) {
  .side { max-height: calc(100vh - 48px); }
  .side > .card:first-child { flex: 1 1 auto; min-height: 0; overflow: hidden; }
  .side > .card:first-child .tree { flex: 1 1 auto; max-height: none; min-height: 120px; }
  .side > .card:last-child { flex: none; }
}
.term { white-space: pre-wrap; overflow-wrap: anywhere; }
.head { display: flex; flex-direction: column; gap: 8px; }
h1 { margin: 0; font-size: 28px; font-weight: 600; letter-spacing: -.01em; }
h2 { margin: 0; font-size: 15px; font-weight: 600; }
.lead { margin: 0; line-height: 1.55; max-width: 640px; }
fieldset { border: 0; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 10px; min-width: 0; }
legend { padding: 0; margin-bottom: 10px; }
.grid { display: grid; gap: 8px; }
.g150 { grid-template-columns: repeat(auto-fit, minmax(min(150px, 100%), 1fr)); }
.g200 { grid-template-columns: repeat(auto-fit, minmax(min(200px, 100%), 1fr)); gap: 10px 16px; }
.g240 { grid-template-columns: repeat(auto-fit, minmax(min(240px, 100%), 1fr)); gap: 14px; }
.g260 { grid-template-columns: repeat(auto-fit, minmax(min(260px, 100%), 1fr)); }
.wrap { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.add { display: flex; gap: 8px; }
.add .inp { width: 170px; }
.opt.tall { padding: 12px 14px; align-items: flex-start; }
.opt.tall input { margin-top: 3px; }
.stack { display: flex; flex-direction: column; gap: 4px; }
.small { font-size: 13px; margin: 0; }
.vnote { margin: 10px 0 0; }
.check { display: flex; align-items: center; gap: 10px; font-size: 14px; }
.dimmed { opacity: .6; }
.card.tight { gap: 12px; padding: 18px; }
.between { display: flex; align-items: baseline; justify-content: space-between; }
.tree { font-size: 13.5px; line-height: 1.75; color: var(--text-code); max-height: 360px; overflow: auto; }
.file { all: unset; cursor: pointer; }
.file:hover, .file.sel { color: var(--accent); }
.file.newf { color: var(--ok); }
.file.newf:hover, .file.newf.sel { color: var(--accent-hover); }
.file:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.viewer { display: flex; flex-direction: column; gap: 6px; border-top: 1px solid var(--line); padding-top: 10px; }
.viewer pre { margin: 0; font-size: 13px; line-height: 1.6; color: var(--text-code); overflow: auto; max-height: 260px; background: var(--bg-inset); padding: 10px; border-radius: var(--r-control); }
.actions { display: flex; flex-wrap: wrap; gap: 8px; padding-top: 4px; }
.grow { flex: 1; }
.pre { white-space: pre-wrap; margin: 0; }
.done { max-width: 640px; }
.done p { margin: 0; }
.next ol { margin: 0; padding-left: 20px; display: flex; flex-direction: column; gap: 8px; line-height: 1.5; }
.body { margin: 0; line-height: 1.55; }
.snippet { margin: 0; font-size: 13px; line-height: 1.6; color: var(--text-code); background: var(--bg-inset); padding: 12px; border-radius: var(--r-control); overflow: auto; }
.vbox { display: flex; flex-direction: column; gap: 12px; margin-top: 2px; }
.outside { margin-top: 8px; padding-top: 8px; border-top: 1px solid var(--line); }
.cmd { overflow-wrap: anywhere; }
.ways summary { cursor: pointer; color: var(--accent); }
.alts { display: flex; flex-direction: column; gap: 10px; margin-top: 10px; padding: 10px; background: var(--bg-inset); border-radius: var(--r-control); }
.strong { font-weight: 600; }
.warn { color: var(--warn); }
</style>
