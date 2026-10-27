<script setup lang="ts">
import { computed } from 'vue'
import type { PlanChange } from '../api/types'

// One tile per planned change, grouped by module. The left edge is the
// action; the fill is how far the apply has got with it.
const props = defineProps<{ changes: PlanChange[]; states: Record<string, { text: string; cls: string }> }>()
const emit = defineEmits<{ (e: 'open', address: string): void }>()

const SYM: Record<string, string> = { create: '+', update: '~', replace: '±', delete: '−', read: '·' }
const NOTE: Record<string, string> = { create: 'create', update: 'update in place', replace: 'replace', delete: 'destroy', read: 'read' }

// aws_instance.web[0] inside module.app → type aws_instance, name web[0]
function short(c: PlanChange) {
  let a = c.address
  if (c.module && a.startsWith(c.module + '.')) a = a.slice(c.module.length + 1)
  const data = a.startsWith('data.')
  if (data) a = a.slice(5)
  if (a.startsWith(c.type + '.')) a = a.slice(c.type.length + 1)
  return { type: (data ? 'data.' : '') + c.type, name: a }
}
const groups = computed(() => {
  const by = new Map<string, PlanChange[]>()
  for (const c of props.changes) by.set(c.module ?? '', [...(by.get(c.module ?? '') ?? []), c])
  return [...by].sort((a, b) => (a[0] === '' ? -1 : b[0] === '' ? 1 : a[0].localeCompare(b[0]))).map(([m, list]) => ({ module: m || 'root module', list }))
})
const count = (cls: string) => props.changes.filter((c) => props.states[c.address]?.cls === cls).length
const pct = (cls: string) => (props.changes.length ? (count(cls) / props.changes.length) * 100 : 0)
</script>

<template>
  <div class="map">
    <div class="tally mono">
      <span><b>{{ count('ok') }}</b> of {{ changes.length }} applied</span>
      <span v-if="count('run')" class="t-run">{{ count('run') }} running</span>
      <span v-if="count('idle')">{{ count('idle') }} waiting</span>
      <span v-if="count('fail')" class="t-fail">{{ count('fail') }} failed</span>
      <span class="bar" role="img" :aria-label="`${count('ok')} of ${changes.length} resources applied`">
        <i class="b-ok" :style="{ width: pct('ok') + '%' }" /><i class="b-run" :style="{ width: pct('run') + '%' }" /><i class="b-fail" :style="{ width: pct('fail') + '%' }" />
      </span>
    </div>
    <div v-for="g in groups" :key="g.module" class="mod">
      <span class="ml mono">{{ g.module }}</span>
      <div class="tiles">
        <button v-for="c in g.list" :key="c.address" type="button" class="tile" :class="['a-' + c.action, 's-' + (states[c.address]?.cls ?? 'idle')]"
          :title="`Show the log lines for ${c.address}`" @click="emit('open', c.address)">
          <span class="ty mono"><span class="sym">{{ SYM[c.action] }}</span> {{ short(c).type }}</span>
          <span class="nm mono">{{ short(c).name }}</span>
          <span class="ft">
            <span class="small muted">{{ NOTE[c.action] ?? c.action }}<template v-if="c.action === 'replace' && c.replace_paths?.length">: {{ c.replace_paths.map((p) => p.join('.')).join(', ') }}</template></span>
            <span class="pill" :class="'st-' + (states[c.address]?.cls ?? 'idle')">{{ states[c.address]?.text ?? 'planned' }}</span>
          </span>
        </button>
      </div>
    </div>
    <div class="legend small">
      <span><i class="sw a-create" />create</span><span><i class="sw a-update" />update</span><span><i class="sw a-replace" />replace</span><span><i class="sw a-delete" />destroy</span>
      <span class="muted">Faded tiles haven't started. Select a tile to see its log lines.</span>
    </div>
  </div>
</template>

<style scoped>
.map { display: flex; flex-direction: column; gap: 14px; }
.small { font-size: 13px; }
.tally { display: flex; flex-wrap: wrap; align-items: center; gap: 14px; font-size: 13px; color: var(--text-muted); }
.tally b { color: var(--text); }
.t-run { color: var(--running); } .t-fail { color: var(--fail); }
.bar { flex: 1 1 160px; height: 6px; border-radius: 3px; background: var(--line); overflow: hidden; display: flex; }
.bar i { display: block; height: 100%; transition: width .3s; }
.b-ok { background: var(--ok); } .b-run { background: var(--running); } .b-fail { background: var(--fail); }
.mod { position: relative; border: 1.5px dashed var(--line-hover); border-radius: 10px; padding: 30px 12px 12px; }
.ml { position: absolute; top: 8px; left: 12px; font-size: 13px; color: var(--text-label); }
.tiles { display: grid; grid-template-columns: repeat(auto-fill, minmax(210px, 1fr)); gap: 10px; }
.tile { all: unset; box-sizing: border-box; display: flex; flex-direction: column; gap: 4px; min-width: 0; padding: 9px 11px; border: 1px solid var(--line-strong); border-left: 4px solid var(--line-hover); border-radius: 8px; background: var(--bg-card); cursor: pointer; transition: opacity .2s, background .2s; }
.tile:hover { border-color: var(--line-hover); }
.tile:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.ty { font-size: 12px; color: var(--text-faint); overflow-wrap: anywhere; }
.nm { font-size: 13px; font-weight: 700; overflow-wrap: anywhere; }
.ft { display: flex; flex-wrap: wrap; justify-content: space-between; align-items: center; gap: 6px; }
.a-create { border-left-color: var(--ok); } .a-update { border-left-color: var(--running); }
.a-replace { border-left-color: var(--warn); } .a-delete { border-left-color: var(--fail); } .a-read { border-left-color: var(--text-label); }
.a-create .sym { color: var(--ok); } .a-update .sym { color: var(--running); } .a-replace .sym { color: var(--warn); } .a-delete .sym { color: var(--fail); }
.sym { font-weight: 700; }
.s-idle { opacity: .66; border-style: dashed; border-left-style: solid; }
.s-run { box-shadow: 0 0 0 2px var(--running-line); animation: pulse 1.6s ease-in-out infinite; }
.s-fail { background: var(--fail-bg); border-color: var(--fail-line); }
.s-ok { background: color-mix(in srgb, var(--ok) 5%, var(--bg-card)); }
@keyframes pulse { 50% { box-shadow: 0 0 0 5px var(--running-line); } }
@media (prefers-reduced-motion: reduce) { .s-run { animation: none; } }
.pill { font: 600 12px var(--font-mono); padding: 2px 7px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; white-space: nowrap; }
.st-ok { color: var(--ok); background: color-mix(in srgb, var(--ok) 10%, transparent); } .st-fail { color: var(--fail); background: color-mix(in srgb, var(--fail) 10%, transparent); }
.st-run { color: var(--running); background: color-mix(in srgb, var(--running) 12%, transparent); } .st-idle { color: var(--text-muted); background: color-mix(in srgb, var(--text-muted) 10%, transparent); }
.legend { display: flex; flex-wrap: wrap; gap: 6px 16px; color: var(--text-muted); }
.legend > span { display: inline-flex; align-items: center; gap: 6px; }
.sw { width: 10px; height: 10px; border-radius: 2px; }
.sw.a-create { background: var(--ok); } .sw.a-update { background: var(--running); } .sw.a-replace { background: var(--warn); } .sw.a-delete { background: var(--fail); }
</style>
