<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import GraphCanvas, { type GNode } from './GraphCanvas.vue'
import { api } from '../api/client'
import type { Architecture } from '../api/types'

const props = defineProps<{ path: string; buffer: string; cursorLine: number }>()
const emit = defineEmits<{ (e: 'goto', file: string, line: number): void }>()

const roots = ref<{ root: string; env: string }[]>([])
const choice = ref('')
const arch = ref<Architecture | null>(null)
const error = ref('')
const loading = ref(false)

async function loadRoots() {
  const r = await api<{ roots: { root: string; env: string }[] }>('/graph/roots?file=' + encodeURIComponent(props.path))
  roots.value = r.roots
  if (!r.roots.some((x) => `${x.root}|${x.env}` === choice.value)) choice.value = r.roots[0] ? `${r.roots[0].root}|${r.roots[0].env}` : ''
}

let timer: number | undefined
let seq = 0
async function refresh() {
  if (!choice.value) return
  const mine = ++seq
  const [root, env] = choice.value.split('|')
  loading.value = true
  try {
    const r = await api<{ architecture: Architecture }>('/graph/architecture', { body: { root, env, buffers: { [props.path]: props.buffer } } })
    if (mine === seq) { arch.value = r.architecture; error.value = '' }
  } catch (e) {
    if (mine === seq) error.value = e instanceof Error ? e.message : String(e)
  } finally { if (mine === seq) loading.value = false }
}
const later = () => { window.clearTimeout(timer); timer = window.setTimeout(refresh, 400) }
onMounted(async () => { await loadRoots(); refresh() })
watch(() => props.path, async () => { await loadRoots(); refresh() })
watch(() => props.buffer, later)
watch(choice, refresh)

// scope: the module the current file belongs to; other resources are dimmed
const fileNodes = computed(() => (arch.value?.nodes ?? []).filter((n) => n.file === props.path))
const scope = computed(() => fileNodes.value[0]?.module ?? (fileNodes.value.length ? '' : null))
const nodes = computed<GNode[]>(() => (arch.value?.nodes ?? []).map((n) => ({
  id: n.id, title: (n.kind === 'data' ? 'data.' : '') + n.type, label: n.name, count: n.count,
  state: n.drift ? 'drift' : n.action ?? '', errors: n.errors, dim: scope.value !== null && (n.module ?? '') !== scope.value,
})))
const containers = computed(() => (arch.value?.containers ?? []).map((c) => ({ ...c, label: c.label })))
const focus = computed(() => fileNodes.value.find((n) => props.cursorLine >= n.line && props.cursorLine <= n.end_line)?.id ?? '')
function select(id: string) {
  const n = arch.value?.nodes.find((x) => x.id === id)
  if (n) emit('goto', n.file, n.line)
}
const graphHref = computed(() => { const [root, env] = choice.value.split('|'); return { path: '/graph', query: { root, env } } })
const canvas = ref<InstanceType<typeof GraphCanvas>>()
const broken = computed(() => arch.value?.errors?.includes(props.path))
</script>

<template>
  <section class="arch" aria-label="Infrastructure view">
    <div class="abar">
      <span class="mono scope">{{ scope === null ? 'no resources in this file' : scope || 'root module' }}</span>
      <label class="small muted asby">as used by
        <select v-model="choice" class="sel" aria-label="Environment">
          <option v-for="r in roots" :key="r.root + r.env" :value="`${r.root}|${r.env}`">{{ r.env }} · {{ r.root }}</option>
        </select>
      </label>
      <div class="seg-group" role="group" aria-label="Diagram type">
        <button class="seg on" type="button" aria-pressed="true">Architecture</button>
        <RouterLink class="seg" :to="graphHref">Dependencies</RouterLink>
      </div>
      <button class="btn xs" type="button" @click="canvas?.fit()">Fit</button>
    </div>
    <p v-if="!roots.length" class="pad muted small">This file isn't used by any configured Terraform root.</p>
    <p v-else-if="error" class="pad err small">{{ error }}</p>
    <template v-else>
      <p v-if="broken" class="warnbar small" role="status">This file has syntax errors; the diagram shows what parses.</p>
      <div class="cv"><GraphCanvas ref="canvas" :nodes="nodes" :edges="arch?.edges ?? []" :containers="containers" :focus="focus" direction="DOWN" aria-label="Architecture" @select="select" /></div>
      <div v-if="arch?.unmapped.length" class="tray small"><span class="lbl">Not placed</span> <span class="mono">{{ arch.unmapped.join(', ') }}</span></div>
    </template>
  </section>
</template>

<style scoped>
.arch { display: flex; flex-direction: column; min-height: 0; height: 100%; border-left: 1px solid var(--line); background: var(--bg-inset); }
.abar { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 10px 14px; border-bottom: 1px solid var(--line); background: #0F1315; }
.scope { font-size: 13.5px; font-weight: 700; }
.asby { display: flex; align-items: center; gap: 6px; }
.sel { height: 28px; background: #111518; color: var(--text); border: 1px solid var(--line-strong); border-radius: 6px; padding: 0 6px; font-size: 13px; }
.seg-group { margin-left: auto; display: flex; gap: 2px; padding: 2px; border: 1px solid var(--line-strong); border-radius: 7px; background: #111518; }
.seg { font-size: 13px; height: 26px; padding: 0 10px; border: 0; border-radius: 5px; background: transparent; color: var(--text-muted); cursor: pointer; display: inline-flex; align-items: center; text-decoration: none; }
.seg.on { background: var(--line-strong); color: var(--text); }
.btn.xs { height: 26px; font-size: 12px; padding: 0 8px; }
.cv { flex: 1; min-height: 300px; }
.small { font-size: 13px; } .pad { padding: 16px; margin: 0; }
.warnbar { margin: 0; padding: 6px 14px; background: #1D1810; color: #F5D49A; border-bottom: 1px solid var(--line); }
.tray { padding: 8px 14px; border-top: 1px solid var(--line); color: var(--text-muted); }
</style>
