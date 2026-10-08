<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import SetupShell from '../components/SetupShell.vue'
import { api, ApiError } from '../api/client'
import type { DetectResponse, Env, TFDir, AnsibleProject } from '../api/types'
import { useWorkspace } from '../stores/workspace'

const router = useRouter()
const store = useWorkspace()

const data = ref<DetectResponse | null>(null)
const loadError = ref('')
const excluded = reactive(new Set<string>())
const prodApproval = ref(true)
const lint = reactive(new Set<string>()) // "kind|dir"
const gitignore = ref(false)
const yaml = ref('')
const saving = ref(false)
const saveError = ref('')

const report = computed(() => data.value?.report)
const managed = computed(() => {
  const r = report.value
  if (!r) return 0
  return [...r.terraform_roots, ...r.terraform_modules, ...r.ansible].filter((x) => !excluded.has(x.path)).length
})

async function loadConfig() {
  const seq = ++cfgSeq
  try {
    const res = await api<{ config_yaml: string }>('/setup/config', {
      body: { exclude: [...excluded], prod_approval: prodApproval.value },
    })
    if (seq === cfgSeq) yaml.value = res.config_yaml
  } catch (e) {
    if (seq === cfgSeq) saveError.value = e instanceof Error ? e.message : String(e)
  }
}
let cfgSeq = 0

onMounted(async () => {
  try {
    data.value = await api<DetectResponse>('/setup/detect')
    if (data.value.exists) return router.replace('/')
    await loadConfig() // initial preview honours the default selections (prod approval on)
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  }
})

watch([() => [...excluded].join('\n'), prodApproval], loadConfig)

function toggle(path: string, on: boolean) {
  if (on) excluded.delete(path)
  else excluded.add(path)
}
function toggleLint(kind: string, dir: string, on: boolean) {
  const k = `${kind}|${dir}`
  if (on) lint.add(k)
  else lint.delete(k)
}

const count = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`

function tfEvidence(d: TFDir): [string, string][] {
  const rows: [string, string][] = []
  if (d.kind === 'module') {
    rows.push(['found by', d.evidence.map((e) => e.note).join(' · ')])
    rows.push(['checks', 'validated through its root, linted on its own'])
    return rows
  }
  rows.push(['found by', d.evidence.filter((e) => e.file !== d.path).map((e) => `${e.note} in ${e.file.split('/').pop()}${e.line ? ':' + e.line : ''}`).join(' · ') || d.evidence.map((e) => e.note).join(' · ')])
  if (d.providers?.length) rows.push(['providers', d.providers.join(' · ')])
  if (d.backend) rows.push(['backend', d.backend])
  if (d.tfvars?.length) rows.push(['variables', d.tfvars.join(' · ')])
  const ci = report.value?.ci.find((c) => c.tool !== 'ansible-playbook' && c.working_dir === d.path)
  if (ci) rows.push(['run from', `${d.path} (from ${ci.file} working-directory)`])
  return rows
}
function ansEvidence(p: AnsibleProject): [string, string][] {
  const rows: [string, string][] = []
  const found = [p.config ? 'ansible.cfg' : '', p.playbooks?.length ? 'playbooks with hosts: + tasks:' : '', p.roles?.length ? 'roles/' : ''].filter(Boolean)
  rows.push(['found by', found.join(' · ') || 'inventory and vars files'])
  if (p.inventories?.length) rows.push(['inventory', p.inventories.map((i) => i.replace(p.path + '/', '')).join(' · ')])
  if (p.playbooks?.length) rows.push(['playbooks', p.playbooks.map((i) => i.split('/').pop()).join(' · ')])
  if (p.roles?.length) rows.push(['roles', p.roles.map((i) => i.split('/').pop()).join(' · ')])
  return rows
}

function envTf(e: Env) {
  return (e.terraform ?? []).map((t) => {
    const [path, ws] = t.split('#')
    return ws ? `${path} (workspace ${ws})` : path
  })
}
function envStatus(e: Env): { text: string; cls: string } {
  const tf = !!e.terraform?.length, an = !!e.ansible?.length
  if (tf && an) return { text: 'matched', cls: 'ok' }
  return { text: tf ? 'terraform only' : 'ansible only', cls: 'warn' }
}

const terminal = computed(() => {
  const r = report.value
  if (!r) return []
  const lines: { mark?: string; cls?: string; text: string }[] = [
    { text: '$ groundwork' },
    { text: `Scanned ${count(r.file_count, 'file')}${r.truncated ? ' (stopped at the file limit)' : ''} in ${store.ws?.name ?? 'this repo'}` },
  ]
  for (const d of r.terraform_roots) lines.push({ mark: '✓', cls: 'ok', text: `Terraform  ${d.path}  root${d.backend ? ' · ' + d.backend + ' backend' : ''}` })
  for (const d of r.terraform_modules) lines.push({ mark: '✓', cls: 'ok', text: `Terraform  ${d.path}  module` })
  for (const p of r.ansible) lines.push({ mark: '✓', cls: 'ok', text: `Ansible    ${p.path}  ${count(p.playbooks?.length ?? 0, 'playbook')} · ${count(p.roles?.length ?? 0, 'role')}` })
  for (const c of r.ci) lines.push({ mark: '·', cls: 'muted', text: `CI         ${c.file} calls ${c.tool}${c.working_dir && c.working_dir !== '.' ? ' in ' + c.working_dir : ''}` })
  return lines
})

async function start() {
  saving.value = true
  saveError.value = ''
  try {
    await api('/setup/apply', {
      body: {
        yaml: yaml.value,
        gitignore: gitignore.value,
        lint_configs: [...lint].map((k) => {
          const [kind, dir] = k.split('|')
          return { kind, dir }
        }),
      },
    })
    await store.refresh()
    router.push('/')
  } catch (e) {
    if (e instanceof ApiError && Array.isArray(e.details)) {
      saveError.value = (e.details as { line?: number; path: string; message: string }[])
        .map((p) => `${p.line ? 'line ' + p.line + ': ' : ''}${p.path ? p.path + ': ' : ''}${p.message}`).join('\n')
    } else saveError.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <SetupShell title="Set up repository">
    <p v-if="loadError" class="err" role="alert">{{ loadError }}</p>
    <template v-else-if="report">
      <div class="main">
        <div class="head">
          <h1>Found infrastructure code in this repo</h1>
          <p class="muted lead">
            <template v-if="report.mode === 'embedded'">
              This looks like an application repo, so groundwork leaves your layout alone. It only manages the paths below and records them in
              <span class="mono">groundwork.yaml</span>. Everything else stays hidden in the Code view unless you switch to All files.
            </template>
            <template v-else>
              This looks like a dedicated infrastructure repo. groundwork manages the paths below and records them in
              <span class="mono">groundwork.yaml</span>.
            </template>
          </p>
        </div>

        <pre class="term" aria-label="Scan output"><template v-for="(l, i) in terminal" :key="i"><span v-if="l.mark" :class="l.cls">{{ l.mark }}</span><span v-if="l.mark"> </span><span :class="l.cls === 'muted' ? 'muted' : ''">{{ l.text }}</span>
</template></pre>

        <section aria-label="Detected projects" class="stack">
          <h2>What groundwork will manage</h2>

          <article v-for="d in [...report.terraform_roots, ...report.terraform_modules]" :key="d.path" class="card" :style="{ opacity: excluded.has(d.path) ? 0.55 : 1 }">
            <div class="row">
              <input type="checkbox" :checked="!excluded.has(d.path)" :aria-label="`Manage ${d.path}`" @change="toggle(d.path, ($event.target as HTMLInputElement).checked)" />
              <span class="kind k-tf">terraform {{ d.kind }}</span>
              <span class="mono path">{{ d.path }}/</span>
            </div>
            <dl class="ev">
              <template v-for="[k, v] in tfEvidence(d)" :key="k"><dt>{{ k }}</dt><dd>{{ v }}</dd></template>
            </dl>
          </article>

          <article v-for="p in report.ansible" :key="p.path" class="card" :style="{ opacity: excluded.has(p.path) ? 0.55 : 1 }">
            <div class="row">
              <input type="checkbox" :checked="!excluded.has(p.path)" :aria-label="`Manage ${p.path}`" @change="toggle(p.path, ($event.target as HTMLInputElement).checked)" />
              <span class="kind k-an">ansible project</span>
              <span class="mono path">{{ p.path }}/</span>
            </div>
            <dl class="ev">
              <template v-for="[k, v] in ansEvidence(p)" :key="k"><dt>{{ k }}</dt><dd>{{ v }}</dd></template>
            </dl>
          </article>

          <article v-for="c in report.ci" :key="c.file + c.line + c.tool" class="card dim">
            <div class="row">
              <span class="kind k-ci">ci workflow</span>
              <span class="mono path">{{ c.file }}</span>
              <span class="aside">read-only</span>
            </div>
            <p class="muted small">
              Calls <span class="mono">{{ c.tool }}</span>{{ c.working_dir && c.working_dir !== '.' ? ' in ' + c.working_dir : '' }}.
              Shown next to runs so local and CI applies line up. groundwork never edits it.
            </p>
          </article>
        </section>

        <section v-if="report.envs.length" class="card" aria-labelledby="map">
          <div>
            <h2 id="map">Environments</h2>
            <span class="muted small">Matched by name across Terraform and Ansible.</span>
          </div>
          <div class="scroll">
            <table class="mono">
              <thead><tr><th>environment</th><th>terraform</th><th>ansible</th><th>status</th></tr></thead>
              <tbody>
                <tr v-for="e in report.envs" :key="e.name">
                  <td class="b">{{ e.name }}</td>
                  <td>{{ envTf(e).join(', ') || '—' }}</td>
                  <td>{{ (e.ansible ?? []).join(', ') || '—' }}</td>
                  <td :class="envStatus(e).cls">{{ envStatus(e).text }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <label class="check"><input v-model="prodApproval" type="checkbox" />Require approval before applying to prod</label>
        </section>
      </div>

      <aside class="side" aria-label="Config preview">
        <div class="card tight">
          <div class="between">
            <h2>groundwork.yaml</h2>
            <span class="mono small ok">only new file</span>
          </div>
          <pre class="mono yaml">{{ yaml }}</pre>
        </div>
        <div class="card tight">
          <span class="lbl">Also add (optional)</span>
          <label v-for="r in report.terraform_roots.filter((r) => !excluded.has(r.path))" :key="'t' + r.path" class="check">
            <input type="checkbox" @change="toggleLint('tflint', r.path, ($event.target as HTMLInputElement).checked)" />.tflint.hcl in {{ r.path }}
          </label>
          <label v-for="p in report.ansible.filter((p) => !excluded.has(p.path))" :key="'a' + p.path" class="check">
            <input type="checkbox" @change="toggleLint('ansible-lint', p.path, ($event.target as HTMLInputElement).checked)" />.ansible-lint in {{ p.path }}
          </label>
          <label class="check"><input v-model="gitignore" type="checkbox" />Add .groundwork/ to .gitignore</label>
          <span class="muted small">.groundwork/ already ignores itself, so this is only for visibility.</span>
          <p v-if="saveError" class="err pre" role="alert">{{ saveError }}</p>
          <div class="actions">
            <button class="btn primary grow" type="button" :disabled="saving || managed === 0" @click="start">Start managing</button>
            <RouterLink class="btn" to="/setup/scaffold">Scaffold instead</RouterLink>
          </div>
          <span class="muted small">Same as <span class="mono">groundwork init --detect</span></span>
        </div>
      </aside>
    </template>
    <p v-else class="muted">Scanning…</p>
  </SetupShell>
</template>

<style scoped>
.main { flex: 999 1 560px; min-width: 0; display: flex; flex-direction: column; gap: 20px; }
.side { flex: 1 1 360px; max-width: 100%; display: flex; flex-direction: column; gap: 14px; }
.head { display: flex; flex-direction: column; gap: 8px; }
h1 { margin: 0; font-size: 28px; font-weight: 600; letter-spacing: -.01em; }
h2 { margin: 0; font-size: 15px; font-weight: 600; }
.lead { margin: 0; line-height: 1.55; max-width: 660px; }
.stack { display: flex; flex-direction: column; gap: 12px; }
.row { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.between { display: flex; align-items: baseline; justify-content: space-between; }
.path { font-size: 15px; font-weight: 700; word-break: break-all; }
.aside { margin-left: auto; font-size: 13px; color: var(--text-label); }
.kind { font: 500 12px var(--font-mono); padding: 3px 8px; border-radius: 999px; border: 1px solid var(--line-strong); }
.k-tf { color: #B79CFF; border-color: #3A3260; }
.k-an { color: var(--warn); border-color: #4A3B1A; }
.k-ci { color: var(--text-muted); }
.ev { display: grid; grid-template-columns: max-content 1fr; gap: 6px 16px; margin: 0; font-size: 14px; }
.ev dt { color: var(--text-label); }
.ev dd { margin: 0; font-family: var(--font-mono); font-size: 13.5px; color: var(--text-code); word-break: break-word; }
.dim { opacity: .85; }
.small { font-size: 13px; margin: 0; }
.scroll { overflow-x: auto; }
table { width: 100%; min-width: 560px; border-collapse: collapse; font-size: 13.5px; }
th { text-align: left; color: var(--text-label); font-weight: 500; padding: 6px 8px; border-bottom: 1px solid var(--line); }
td { padding: 8px; border-bottom: 1px solid var(--line); }
.b { font-weight: 700; }
.check { display: flex; align-items: center; gap: 10px; font-size: 14px; }
.card.tight { gap: 12px; padding: 18px; }
.yaml { margin: 0; font-size: 13px; line-height: 1.7; color: var(--text-code); overflow-x: auto; max-height: 420px; }
.actions { display: flex; flex-wrap: wrap; gap: 8px; padding-top: 4px; }
.grow { flex: 1; }
.pre { white-space: pre-wrap; margin: 0; }
</style>
