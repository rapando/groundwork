<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Diagnostic, UnitStatus } from '../api/types'

const props = defineProps<{
  file: string
  fileDiagnostics: Diagnostic[]
  allDiagnostics: Diagnostic[]
  unit?: UnitStatus
  running: boolean
}>()
const emit = defineEmits<{
  (e: 'goto', d: Diagnostic): void
  (e: 'fix', d: Diagnostic): void
  (e: 'run'): void
}>()

const tab = ref<'file' | 'all'>('file')
const shown = computed(() => (tab.value === 'file' ? props.fileDiagnostics : props.allDiagnostics))
const sevLabel = { error: 'error', warning: 'warn', info: 'info' } as const
const ranAny = computed(() => props.unit?.results.some((r) => r.status !== 'idle') ?? false)
const statusText = (r: UnitStatus['results'][number]) => {
  switch (r.status) {
    case 'ok': return '✓'
    case 'issues': return `${r.count}`
    case 'running': return '…'
    case 'skipped': return 'skipped'
    case 'failed': return 'failed'
    default: return 'not run'
  }
}
</script>

<template>
  <section class="tray" aria-label="Problems">
    <header class="bar">
      <div class="tabs" role="tablist">
        <button class="tab" :class="{ on: tab === 'file' }" role="tab" :aria-selected="tab === 'file'" @click="tab = 'file'">This file ({{ fileDiagnostics.length }})</button>
        <button class="tab" :class="{ on: tab === 'all' }" role="tab" :aria-selected="tab === 'all'" @click="tab = 'all'">All problems ({{ allDiagnostics.length }})</button>
      </div>
      <div v-if="unit" class="chips">
        <span v-for="r in unit.results" :key="r.tool" class="chip mono" :class="r.status" :title="[r.message, r.hint].filter(Boolean).join(' — ')">
          {{ r.tool }} <b>{{ statusText(r) }}</b>
        </span>
      </div>
      <button class="btn sm run" type="button" :disabled="running" @click="emit('run')">{{ running ? 'Checking…' : 'Run checks' }}</button>
    </header>

    <div v-if="unit" class="notes">
      <template v-for="r in unit.results" :key="r.tool">
        <p v-if="r.status === 'skipped'" class="note"><span class="warn">!</span> {{ r.tool }} skipped: {{ r.message }}<template v-if="r.hint"> — <span class="mono">{{ r.hint }}</span></template></p>
        <p v-else-if="r.status === 'failed'" class="note fail" role="alert">✕ {{ r.tool }} could not run: {{ r.message }}<template v-if="r.hint"> — {{ r.hint }}</template></p>
      </template>
    </div>

    <ul class="list">
      <li v-for="d in shown" :key="d.id" class="item" :class="d.severity">
        <span class="pill" :class="d.severity">{{ sevLabel[d.severity] }}</span>
        <span class="tool mono">{{ d.tool }}<template v-if="d.code"> · {{ d.code }}</template></span>
        <button type="button" class="msg" @click="emit('goto', d)">
          <span>{{ d.message }}</span>
          <span v-if="d.detail" class="muted detail">{{ d.detail }}</span>
        </button>
        <span class="loc mono">{{ tab === 'all' ? d.file : '' }}{{ d.line ? (tab === 'all' ? ':' : 'Ln ') + d.line : '' }}</span>
        <a v-if="d.link" :href="d.link" target="_blank" rel="noopener noreferrer" class="docs">docs</a>
        <button v-if="d.fix" type="button" class="btn sm fix" @click="emit('fix', d)">{{ d.fix.title }}</button>
      </li>
    </ul>
    <p v-if="!shown.length" class="empty muted">
      <template v-if="running">Running checks…</template>
      <template v-else-if="unit && !ranAny">Checks haven't run for this unit yet. Press “Run checks”, or save a file.</template>
      <template v-else-if="!unit && tab === 'file'">This file isn't part of a managed unit, so no checks apply.</template>
      <template v-else>No problems found.</template>
    </p>
  </section>
</template>

<style scoped>
.tray { display: flex; flex-direction: column; min-height: 0; border-top: 1px solid var(--line); background: #0F1315; }
.bar { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 0 12px; border-bottom: 1px solid var(--line); }
.tabs { display: flex; gap: 14px; }
.tab { background: transparent; border: 0; border-bottom: 2px solid transparent; color: var(--text-muted); height: 38px; padding: 0 4px; cursor: pointer; font-size: 12px; }
.tab.on { color: var(--text); border-bottom-color: var(--accent); }
.chips { display: flex; flex-wrap: wrap; gap: 6px; margin-left: 4px; }
.chip { font-size: 11px; padding: 2px 8px; border-radius: 4px; background: rgba(154, 164, 171, .10); color: var(--text-muted); }
.chip.ok { color: var(--ok); background: rgba(95, 211, 141, .10); }
.chip.issues { color: var(--warn); background: rgba(245, 182, 71, .10); }
.chip.failed { color: var(--fail); background: rgba(255, 122, 122, .10); }
.chip.running { color: var(--running); background: rgba(124, 192, 255, .12); }
.run { margin-left: auto; height: 28px; font-size: 12px; }
.notes { padding: 6px 14px 0; }
.note { margin: 4px 0; font-size: 12.5px; color: var(--text-muted); }
.list { list-style: none; margin: 0; padding: 4px 0; overflow: auto; }
.item { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 12px; padding: 8px 14px; border-bottom: 1px solid #161B1E; }
.pill { font: 500 11px var(--font-mono); text-transform: uppercase; letter-spacing: .04em; padding: 3px 8px; border-radius: 4px; }
.pill.error { color: var(--fail); background: rgba(255, 122, 122, .10); }
.pill.warning { color: var(--warn); background: rgba(245, 182, 71, .10); }
.pill.info { color: var(--running); background: rgba(124, 192, 255, .12); }
.tool { font-size: 11.5px; color: var(--text-label); }
.msg { all: unset; cursor: pointer; display: flex; flex-direction: column; gap: 2px; flex: 1 1 260px; min-width: 0; font-size: 13px; }
.msg:hover > span:first-child { text-decoration: underline; }
.msg:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.detail { font-size: 12px; }
.loc { font-size: 11.5px; color: var(--text-label); }
.docs { font-size: 12px; }
.fix { height: 28px; font-size: 12px; }
.empty { margin: 0; padding: 14px; font-size: 13px; }
</style>
