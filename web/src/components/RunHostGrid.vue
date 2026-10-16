<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { HostResult } from '../api/types'

// Tasks down, hosts across: each cell is the task's result on that host (the
// worst, when a loop or handler reports several). Selecting a cell shows its
// output and diff.
const props = defineProps<{ results: HostResult[]; hosts: { host: string; worst: string }[] }>()
const emit = defineEmits<{ (e: 'open', host: string): void }>()

const RANK: Record<string, number> = { skipped: 0, ok: 1, changed: 2, failed: 3, unreachable: 4 }
const rank = (r: HostResult) => (r.status === 'failed' && r.ignored ? RANK.ok : RANK[r.status])
const CLS: Record<string, string> = { ok: 'ok', changed: 'warn', failed: 'fail', unreachable: 'fail', skipped: 'idle' }

const rows = computed(() => {
  const by = new Map<string, { key: string; play: string; task: string; cells: Map<string, HostResult[]> }>()
  for (const r of props.results) {
    const key = r.play + '\0' + r.task
    const row = by.get(key) ?? { key, play: r.play, task: r.task, cells: new Map() }
    row.cells.set(r.host, [...(row.cells.get(r.host) ?? []), r])
    by.set(key, row)
  }
  return [...by.values()]
})
const plays = computed(() => new Set(rows.value.map((r) => r.play)).size > 1)
const worst = (l: HostResult[]) => l.reduce((a, b) => (rank(b) > rank(a) ? b : a))
const label = (r: HostResult) => r.status + (r.ignored ? ' (ignored)' : '')
const cellCls = (r: HostResult) => (r.status === 'failed' && r.ignored ? 'ok' : CLS[r.status])

const open = ref<{ key: string; host: string } | null>(null)
watch(() => props.results.length === 0, (empty) => { if (empty) open.value = null })
const picked = computed(() => (open.value ? rows.value.find((r) => r.key === open.value!.key)?.cells.get(open.value.host) ?? [] : []))
const pickedRow = computed(() => rows.value.find((r) => r.key === open.value?.key))
function toggle(key: string, host: string) {
  open.value = open.value?.key === key && open.value.host === host ? null : { key, host }
}
const tally = computed(() => {
  const t = { ok: 0, changed: 0, failed: 0 }
  for (const r of props.results) {
    if (r.status === 'changed') t.changed++
    else if (r.status === 'failed' && !r.ignored || r.status === 'unreachable') t.failed++
    else if (r.status !== 'skipped') t.ok++
  }
  return t
})
</script>

<template>
  <div class="grid-wrap">
    <div class="tally mono">
      <span><b>{{ hosts.length }}</b> host{{ hosts.length === 1 ? '' : 's' }} · {{ rows.length }} task{{ rows.length === 1 ? '' : 's' }}</span>
      <span class="t-ok">{{ tally.ok }} ok</span><span class="t-warn">{{ tally.changed }} changed</span><span v-if="tally.failed" class="t-fail">{{ tally.failed }} failed</span>
    </div>
    <div class="scroll">
      <table class="matrix mono">
        <thead>
          <tr>
            <th class="tk">task</th>
            <th v-for="h in hosts" :key="h.host" class="hc">
              <button type="button" class="hbtn" :title="`Show the log lines for ${h.host}`" @click="emit('open', h.host)">{{ h.host }}</button>
              <span class="pill" :class="'st-' + CLS[h.worst]">{{ h.worst }}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in rows" :key="r.key">
            <th class="tk" :title="r.play"><span v-if="plays" class="play">{{ r.play }} › </span>{{ r.task }}</th>
            <td v-for="h in hosts" :key="h.host">
              <button v-if="r.cells.get(h.host)" type="button" class="cell" :class="['c-' + cellCls(worst(r.cells.get(h.host)!)), { on: open?.key === r.key && open?.host === h.host }]"
                :aria-label="`${r.task} on ${h.host}: ${label(worst(r.cells.get(h.host)!))}`" :aria-expanded="open?.key === r.key && open?.host === h.host" @click="toggle(r.key, h.host)">
                {{ label(worst(r.cells.get(h.host)!)) }}</button>
              <span v-else class="cell c-none" aria-label="did not run">—</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-if="picked.length" class="detail" role="region" :aria-label="`Result of ${pickedRow?.task} on ${open?.host}`">
      <div class="between"><span class="small"><b class="mono">{{ open?.host }}</b> · {{ pickedRow?.task }}</span><button class="btn sm" type="button" @click="open = null">Close</button></div>
      <div v-for="(t, i) in picked" :key="i" class="task">
        <span class="pill" :class="'st-' + cellCls(t)">{{ label(t) }}</span>
        <pre v-if="t.msg" class="tmsg">{{ t.msg }}</pre>
        <pre v-if="t.diff" class="tdiff">{{ t.diff }}</pre>
        <span v-if="!t.msg && !t.diff" class="small muted">No output.</span>
      </div>
    </div>
    <div class="legend small">
      <span><i class="sw c-ok" />ok</span><span><i class="sw c-warn" />changed</span><span><i class="sw c-fail" />failed</span><span><i class="sw c-idle" />skipped</span>
      <span class="muted">Select a cell for the task's output and diff, or a host name for its log lines.</span>
    </div>
  </div>
</template>

<style scoped>
.grid-wrap { display: flex; flex-direction: column; gap: 14px; }
.small { font-size: 12.5px; }
.tally { display: flex; flex-wrap: wrap; gap: 14px; font-size: 12.5px; color: var(--text-muted); }
.tally b { color: var(--text); }
.t-ok { color: var(--ok); } .t-warn { color: var(--warn); } .t-fail { color: var(--fail); }
.scroll { overflow: auto; max-width: 100%; }
.matrix { border-collapse: separate; border-spacing: 4px; font-size: 12px; }
.matrix th { font-weight: 500; color: var(--text-label); text-align: left; padding: 2px 6px; white-space: nowrap; vertical-align: bottom; }
.tk { max-width: 340px; overflow: hidden; text-overflow: ellipsis; }
.play { color: var(--text-faint); }
.hc { text-align: center !important; }
.hc .pill { display: inline-block; margin-top: 4px; }
.hbtn { all: unset; display: block; cursor: pointer; color: var(--text); font-weight: 700; text-align: center; }
.hbtn:hover { text-decoration: underline; }
.hbtn:focus-visible, .cell:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.cell { all: unset; box-sizing: border-box; display: block; min-width: 96px; height: 28px; line-height: 26px; padding: 0 8px; border-radius: 5px; text-align: center; font: 500 11px var(--font-mono); text-transform: uppercase; letter-spacing: .04em; cursor: pointer; border: 1px solid transparent; white-space: nowrap; }
.cell.on { box-shadow: 0 0 0 2px var(--accent); }
.c-ok { background: var(--ok-bg); color: var(--ok); border-color: var(--ok-line); }
.c-warn { background: var(--warn-bg); color: var(--warn); border-color: var(--warn-line); }
.c-fail { background: var(--fail-bg); color: var(--fail); border-color: var(--fail-line); }
.c-idle { color: var(--text-muted); border: 1px dashed var(--line-strong); }
.c-none { color: var(--text-disabled); cursor: default; border: 1px dashed var(--line); }
.detail { display: flex; flex-direction: column; gap: 8px; padding: 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--bg-card); }
.between { display: flex; justify-content: space-between; align-items: center; gap: 10px; }
.task { display: flex; flex-direction: column; align-items: flex-start; gap: 4px; }
.tmsg, .tdiff { margin: 0; width: 100%; box-sizing: border-box; font: 12.5px/1.5 var(--font-mono); white-space: pre-wrap; overflow-wrap: anywhere; background: var(--bg-inset); padding: 6px 8px; border-radius: 4px; max-height: 240px; overflow: auto; }
.tmsg { color: var(--fail-strong); }
.tdiff { color: var(--text-code); }
.pill { font: 500 11.5px var(--font-mono); padding: 2px 7px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; white-space: nowrap; }
.st-ok { color: var(--ok); background: color-mix(in srgb, var(--ok) 10%, transparent); } .st-fail { color: var(--fail); background: color-mix(in srgb, var(--fail) 10%, transparent); }
.st-warn { color: var(--warn); background: color-mix(in srgb, var(--warn) 10%, transparent); } .st-idle { color: var(--text-muted); background: color-mix(in srgb, var(--text-muted) 10%, transparent); }
.legend { display: flex; flex-wrap: wrap; gap: 6px 16px; color: var(--text-muted); }
.legend > span { display: inline-flex; align-items: center; gap: 6px; }
.sw { width: 12px; height: 12px; border-radius: 3px; }
</style>
