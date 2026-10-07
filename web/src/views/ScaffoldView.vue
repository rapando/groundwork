<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import SetupShell from '../components/SetupShell.vue'
import { api, apiBlob, ApiError, fieldErrors } from '../api/client'
import type { PlannedFile, PreviewResponse, ScaffoldForm, WriteResult } from '../api/types'
import { useWorkspace } from '../stores/workspace'

const router = useRouter()
const store = useWorkspace()

const providers = [
  { id: 'aws-envs', label: 'AWS' },
  { id: 'onprem', label: 'None / on-prem' },
] as const
const comingSoon = ['Google Cloud', 'Azure', 'Hetzner', 'DigitalOcean']

const form = reactive<ScaffoldForm>({
  preset: 'aws-envs', project: '', backend: 's3', bucket: '', region: '',
  envs: ['dev', 'staging', 'prod'], layout: 'dirs', inventory: 'terraform',
  roles: ['common', 'nginx'], vault: true,
  checks: ['fmt', 'validate', 'tflint', 'ansible-lint', 'yamllint'], pre_commit: true, ci: true,
})
const rolesText = ref('common, nginx')
const newEnv = ref('')
const bucketTouched = ref(false)
const gitCommit = ref(false)
const overwrite = reactive(new Set<string>())

const preview = ref<PreviewResponse | null>(null)
const errors = ref<Record<string, string[]>>({})
const selected = ref('')
const busy = ref(false)
const failure = ref('')
const done = ref<{ results: WriteResult[]; git_commit?: string; git_error?: string } | null>(null)

const envChoices = computed(() => [...new Set(['dev', 'staging', 'prod', ...form.envs])])
const err = (f: string) => errors.value[f]?.join(' ')

const checkOptions = [
  { id: 'fmt', label: 'terraform fmt' }, { id: 'validate', label: 'terraform validate' },
  { id: 'tflint', label: 'tflint' }, { id: 'checkov', label: 'checkov' },
  { id: 'ansible-lint', label: 'ansible-lint' }, { id: 'yamllint', label: 'yamllint' },
]

onMounted(async () => {
  const ws = store.ws ?? (await store.refresh())
  form.project = (ws?.name ?? 'infra').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '') || 'infra'
  gitCommit.value = !!ws?.git
})

watch(() => form.project, (p) => { if (!bucketTouched.value) form.bucket = p ? `${p}-tfstate` : '' })
watch(() => form.preset, (p) => {
  if (p === 'onprem' && form.backend === 's3') form.backend = 'local'
  if (p === 'aws-envs' && form.backend === 'local') form.backend = 's3'
})
watch(rolesText, (t) => { form.roles = t.split(',').map((s) => s.trim()).filter(Boolean) })

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
const selectedFile = computed(() => preview.value?.files.find((f) => f.path === selected.value))
const existing = computed(() => preview.value?.files.filter((f) => f.exists && !f.merge) ?? [])
const hasErrors = computed(() => Object.keys(errors.value).length > 0)
const missingTools = computed(() => (store.ws?.tools ?? []).filter((t) => !t.found && (
  ['terraform', 'ansible'].includes(t.name) || form.checks.includes(t.name))))
const toolLine = computed(() => (store.ws?.tools ?? []).filter((t) => ['terraform', 'ansible', 'tflint'].includes(t.name)))

async function create() {
  busy.value = true; failure.value = ''
  try {
    done.value = await api('/setup/scaffold', { body: { form, overwrite: [...overwrite], git_commit: gitCommit.value } })
    await store.refresh()
  } catch (e) {
    failure.value = e instanceof ApiError && e.code === 'invalid_form' ? 'Fix the highlighted fields first.' : e instanceof Error ? e.message : String(e)
  } finally { busy.value = false }
}
async function download() {
  busy.value = true; failure.value = ''
  try {
    const blob = await apiBlob('/setup/scaffold?format=zip', { form })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `${form.project}-iac.zip`
    a.click()
    URL.revokeObjectURL(a.href)
  } catch (e) { failure.value = e instanceof Error ? e.message : String(e) } finally { busy.value = false }
}
const tally = computed(() => {
  const n: Record<string, number> = {}
  for (const r of done.value?.results ?? []) n[r.status] = (n[r.status] ?? 0) + 1
  return n
})
</script>

<template>
  <SetupShell title="Set up repository">
    <section v-if="done" class="card done" aria-live="polite">
      <h1>Repository scaffolded</h1>
      <p>
        Created {{ tally.created ?? 0 }} files<template v-if="tally.merged">, merged {{ tally.merged }}</template><template v-if="tally.skipped">,
        kept {{ tally.skipped }} existing</template><template v-if="tally.overwritten">, overwrote {{ tally.overwritten }}</template>.
      </p>
      <p v-if="done.git_commit" class="ok">Committed: {{ done.git_commit }}</p>
      <p v-if="done.git_error" class="err pre" role="alert">The files were written, but the commit failed: {{ done.git_error }}</p>
      <p class="muted">Nothing has touched your cloud. Next: initialise Terraform and run a plan from the console.</p>
      <div><button class="btn primary" type="button" @click="router.push('/')">Open console</button></div>
    </section>

    <template v-else>
      <div class="main">
        <div class="head">
          <h1>Scaffold an IaC repo</h1>
          <p class="muted lead">
            No <span class="mono">groundwork.yaml</span> here yet. Pick a layout and groundwork writes the Terraform and Ansible skeleton,
            check config and a CI workflow. Nothing touches your cloud until you run a plan.
          </p>
        </div>

        <pre class="term" aria-label="Environment"><span class="muted">$ </span>groundwork
<template v-for="t in toolLine" :key="t.name"><span :class="t.found ? 'ok' : 'warn'">{{ t.found ? '✓' : '!' }}</span> {{ t.name }}{{ t.found ? ' ' + (t.version ?? '') : ' not found' }}  </template>
No groundwork.yaml in {{ store.ws?.root }} — starting setup</pre>

        <section class="card" aria-labelledby="s-cloud">
          <h2 id="s-cloud">1 · Cloud and state</h2>
          <fieldset>
            <legend class="lbl">Provider</legend>
            <div class="grid g150">
              <label v-for="p in providers" :key="p.id" class="opt" :class="{ on: form.preset === p.id }">
                <input v-model="form.preset" type="radio" name="prov" :value="p.id" />{{ p.label }}
              </label>
              <label v-for="p in comingSoon" :key="p" class="opt disabled" title="Planned; the aws-envs and onprem presets ship first">
                <input type="radio" name="prov" disabled />{{ p }}<span class="soon">soon</span>
              </label>
            </div>
          </fieldset>
          <div class="grid g240">
            <label class="field">Project name
              <input v-model="form.project" class="inp" :class="{ bad: err('project') }" type="text" />
              <span v-if="err('project')" class="err">{{ err('project') }}</span>
            </label>
            <label class="field">State backend
              <select v-model="form.backend" class="inp">
                <option v-if="form.preset === 'aws-envs'" value="s3">S3 + DynamoDB lock</option>
                <option value="local">Local file</option>
                <option value="http">HTTP backend</option>
              </select>
              <span v-if="err('backend')" class="err">{{ err('backend') }}</span>
            </label>
            <label v-if="form.backend === 's3'" class="field">Bucket name
              <input v-model="form.bucket" class="inp" :class="{ bad: err('bucket') }" type="text" @input="bucketTouched = true" />
              <span v-if="err('bucket')" class="err">{{ err('bucket') }}</span>
            </label>
            <label v-if="form.preset === 'aws-envs'" class="field">Default region
              <input v-model="form.region" class="inp" :class="{ bad: err('region') }" type="text" placeholder="e.g. eu-west-1" />
              <span v-if="err('region')" class="err">{{ err('region') }}</span>
            </label>
          </div>
        </section>

        <section class="card" aria-labelledby="s-env">
          <h2 id="s-env">2 · Environments</h2>
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
            <legend class="lbl">Layout</legend>
            <div class="grid g260">
              <label class="opt tall" :class="{ on: form.layout === 'dirs' }">
                <input v-model="form.layout" type="radio" name="lay" value="dirs" />
                <span class="stack"><span>Directory per environment</span><span class="mono small muted">envs/dev, envs/staging, envs/prod</span></span>
              </label>
              <label class="opt tall" :class="{ on: form.layout === 'workspaces' }">
                <input v-model="form.layout" type="radio" name="lay" value="workspaces" />
                <span class="stack"><span>Workspaces</span><span class="mono small muted">one root, terraform workspace per env</span></span>
              </label>
            </div>
          </fieldset>
        </section>

        <section class="card" aria-labelledby="s-ans">
          <h2 id="s-ans">3 · Ansible</h2>
          <div class="grid g240">
            <label class="field">Inventory
              <select v-model="form.inventory" class="inp">
                <option value="terraform">Generated from Terraform outputs</option>
                <option value="static">Static YAML per environment</option>
              </select>
            </label>
            <label class="field">Starter roles
              <input v-model="rolesText" class="inp" :class="{ bad: err('roles') }" type="text" />
              <span v-if="err('roles')" class="err">{{ err('roles') }}</span>
            </label>
          </div>
          <label class="check"><input v-model="form.vault" type="checkbox" />Add ansible-vault example files (group_vars/&lt;env&gt;/vault.yml.example)</label>
        </section>

        <section class="card" aria-labelledby="s-chk">
          <h2 id="s-chk">4 · Checks and CI</h2>
          <div class="grid g200">
            <label v-for="c in checkOptions" :key="c.id" class="check">
              <input type="checkbox" :checked="form.checks.includes(c.id)" @change="toggleCheck(c.id, ($event.target as HTMLInputElement).checked)" />{{ c.label }}
            </label>
            <label class="check"><input v-model="form.pre_commit" type="checkbox" />pre-commit hook</label>
            <label class="check"><input v-model="form.ci" type="checkbox" />GitHub Actions: plan on PR</label>
          </div>
        </section>
      </div>

      <aside class="side" aria-label="Files to be created">
        <div class="card tight">
          <div class="between">
            <h2>Preview</h2>
            <span v-if="preview" class="mono small ok">+{{ preview.new }} files<template v-if="preview.existing"> · {{ preview.existing }} exist</template></span>
          </div>
          <p v-if="hasErrors" class="muted small">Fix the highlighted fields to see the file list.</p>
          <div v-else class="tree mono" role="tree">
            <div v-for="l in lines" :key="(l.file?.path ?? '') + l.name + l.depth" :style="{ paddingLeft: l.depth * 14 + 'px' }">
              <span v-if="l.dir" class="muted">{{ l.name }}</span>
              <button v-else type="button" class="file" :class="{ sel: selected === l.file!.path, newf: !l.file!.exists }" @click="selected = l.file!.path">
                {{ l.name }}<span v-if="l.file!.exists" class="muted"> {{ l.file!.merge ? '(merge)' : '(exists)' }}</span>
              </button>
            </div>
          </div>
          <div v-if="selectedFile" class="viewer">
            <span class="lbl mono">{{ selectedFile.path }}</span>
            <pre class="mono">{{ selectedFile.content }}</pre>
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
            <span v-for="t in missingTools" :key="t.name" class="mono small"><span class="warn">!</span> {{ t.hint }}</span>
          </div>
          <p v-if="failure" class="err pre" role="alert">{{ failure }}</p>
          <div class="actions">
            <button class="btn primary grow" type="button" :disabled="busy || hasErrors || !preview" @click="create">Create files</button>
            <button class="btn" type="button" :disabled="busy || hasErrors || !preview" @click="download">Download as zip</button>
          </div>
          <span class="muted small">Same as <span class="mono">groundwork init --preset {{ form.preset }}</span></span>
        </div>
      </aside>
    </template>
  </SetupShell>
</template>

<style scoped>
.main { flex: 999 1 560px; min-width: 0; display: flex; flex-direction: column; gap: 20px; }
.side { flex: 1 1 340px; max-width: 100%; display: flex; flex-direction: column; gap: 14px; position: sticky; top: 24px; }
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
.soon { margin-left: auto; font-size: 11px; color: var(--text-label); }
.small { font-size: 12px; margin: 0; }
.check { display: flex; align-items: center; gap: 10px; font-size: 13px; }
.dimmed { opacity: .6; }
.card.tight { gap: 12px; padding: 18px; }
.between { display: flex; align-items: baseline; justify-content: space-between; }
.tree { font-size: 12.5px; line-height: 1.75; color: var(--text-code); max-height: 360px; overflow: auto; }
.file { all: unset; cursor: pointer; }
.file:hover, .file.sel { color: var(--accent); }
.file.newf { color: var(--ok); }
.file.newf:hover, .file.newf.sel { color: var(--accent-hover); }
.file:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.viewer { display: flex; flex-direction: column; gap: 6px; border-top: 1px solid var(--line); padding-top: 10px; }
.viewer pre { margin: 0; font-size: 12px; line-height: 1.6; color: var(--text-code); overflow: auto; max-height: 260px; background: var(--bg-inset); padding: 10px; border-radius: var(--r-control); }
.actions { display: flex; flex-wrap: wrap; gap: 8px; padding-top: 4px; }
.grow { flex: 1; }
.pre { white-space: pre-wrap; margin: 0; }
.done { max-width: 640px; }
</style>
