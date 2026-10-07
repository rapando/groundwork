<script setup lang="ts">
// ⌘K / Ctrl-K palette and single-key shortcuts ("g r" → Runs, "?" → help).
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api/client'
import type { InventoryResponse } from '../api/types'
import { useChecks } from '../stores/checks'
import { useFiles } from '../stores/files'
import { useIssues } from '../stores/issues'
import { kindLabel, useRuns } from '../stores/runs'
import { useWorkspace } from '../stores/workspace'

interface Item { id: string; group: string; label: string; hint?: string; run: () => void | Promise<unknown> }

const router = useRouter()
const ws = useWorkspace()
const files = useFiles()
const runs = useRuns()
const checks = useChecks()
const issues = useIssues()

const open = ref(false)
const help = ref(false)
const q = ref('')
const sel = ref(0)
const input = ref<HTMLInputElement | null>(null)
const notice = ref('')
const hosts = ref<{ name: string; project: string; env: string }[]>([])
let hostsAt = 0

const cfg = computed(() => ws.ws?.config)
const hasTF = computed(() => (cfg.value?.terraform?.roots ?? []).length > 0)
const hasAns = computed(() => (cfg.value?.ansible?.projects ?? []).length > 0)

const SCREENS = computed(() => [
  { key: 'o', label: 'Overview', to: '/' },
  { key: 'c', label: 'Code', to: '/code' },
  { key: 'r', label: 'Runs', to: '/runs' },
  ...(hasTF.value ? [{ key: 'g', label: 'Graph', to: '/graph' }] : []),
  ...(hasAns.value ? [{ key: 'i', label: 'Inventory', to: '/inventory' }] : []),
  { key: 'v', label: 'Variables', to: '/variables' },
  { key: 't', label: 'Troubleshoot', to: '/troubleshoot' },
])

function go(to: string) { router.push(to) }
function say(m: string) { notice.value = m; window.setTimeout(() => (notice.value = ''), 4000) }

const items = computed<Item[]>(() => {
  const out: Item[] = []
  for (const s of SCREENS.value) out.push({ id: 'screen:' + s.to, group: 'Go to', label: s.label, hint: 'g ' + s.key, run: () => go(s.to) })
  for (const e of runs.envs) {
    for (const t of e.targets) {
      out.push({ id: `plan:${t.root}:${t.env}`, group: 'Actions', label: `Plan ${t.env}`, hint: t.root, run: async () => { const r = await runs.plan(t.root, t.env); go('/runs/' + r.id) } })
      out.push({ id: `graph:${t.root}:${t.env}`, group: 'Go to', label: `Graph ${t.env}`, hint: t.root, run: () => go(`/graph?root=${encodeURIComponent(t.root)}&env=${encodeURIComponent(t.env)}`) })
    }
    for (const a of e.ansible) out.push({ id: `inv:${a.project}:${e.name}`, group: 'Go to', label: `Inventory ${e.name}`, hint: a.project, run: () => go(`/inventory?project=${encodeURIComponent(a.project)}&env=${encodeURIComponent(e.name)}`) })
  }
  out.push({ id: 'checks', group: 'Actions', label: 'Run all checks', run: async () => { await checks.run('', true); say('Checks started') } })
  out.push({ id: 'doctor', group: 'Actions', label: 'Run doctor', run: () => go('/troubleshoot?doctor=1') })
  out.push({ id: 'tour', group: 'Actions', label: 'Show tips', run: () => go('/?tour=1') })
  out.push({ id: 'help', group: 'Actions', label: 'Keyboard shortcuts', hint: '?', run: () => { help.value = true } })
  for (const i of issues.open) out.push({ id: 'issue:' + i.id, group: 'Issues', label: i.title, hint: i.category, run: () => go('/troubleshoot/' + i.id) })
  for (const r of runs.list.slice(0, 30)) out.push({ id: 'run:' + r.id, group: 'Runs', label: `#${r.id} ${kindLabel(r)}`, hint: `${r.target.env} · ${r.status.replace('_', ' ')}`, run: () => go('/runs/' + r.id) })
  for (const h of hosts.value) out.push({ id: `host:${h.project}:${h.env}:${h.name}`, group: 'Hosts', label: h.name, hint: `${h.env} · ${h.project}`, run: () => go(`/inventory?project=${encodeURIComponent(h.project)}&env=${encodeURIComponent(h.env)}&host=${encodeURIComponent(h.name)}`) })
  for (const f of files.files) out.push({ id: 'file:' + f, group: 'Files', label: f, run: () => go('/code/' + f.split('/').map(encodeURIComponent).join('/')) })
  return out
})

// every space-separated word must match as a subsequence; consecutive runs and
// word starts score higher
function score(text: string, query: string): number {
  if (!query) return 1
  let total = 0
  for (const w of query.split(/\s+/).filter(Boolean)) {
    const s = scoreWord(text, w)
    if (!s) return 0
    total += s
  }
  return total - text.length / 100
}
function scoreWord(text: string, query: string): number {
  const t = text.toLowerCase(), qq = query.toLowerCase()
  let ti = 0, s = 0, run = 0
  for (const ch of qq) {
    const found = t.indexOf(ch, ti)
    if (found < 0) return 0
    run = found === ti ? run + 1 : 1
    s += run * 2 + (found === 0 || '/ ._-'.includes(t[found - 1]) ? 3 : 0)
    ti = found + 1
  }
  return s
}
const results = computed(() => {
  const qq = q.value.trim()
  const scored = items.value.map((it) => ({ it, s: Math.max(score(it.label, qq), score((it.hint ?? '') + ' ' + it.label, qq) * 0.8) })).filter((x) => x.s > 0)
  const RANK = ['Go to', 'Actions', 'Issues', 'Runs', 'Hosts', 'Files']
  if (qq) scored.sort((a, b) => b.s - a.s)
  else scored.sort((a, b) => RANK.indexOf(a.it.group) - RANK.indexOf(b.it.group)) // stable: keeps order within a group
  return scored.slice(0, 50).map((x) => x.it)
})
watch(q, () => (sel.value = 0))

async function loadHosts() {
  if (Date.now() - hostsAt < 60_000) return
  hostsAt = Date.now()
  const out: typeof hosts.value = []
  await Promise.all(runs.envs.flatMap((e) => e.ansible.map(async (a) => {
    try {
      const r = await api<InventoryResponse>(`/inventory?project=${encodeURIComponent(a.project)}&env=${encodeURIComponent(e.name)}`)
      for (const h of r.hosts ?? []) out.push({ name: h.name, project: a.project, env: e.name })
    } catch { /* inventory unavailable */ }
  })))
  hosts.value = out
}

async function show() {
  open.value = true
  q.value = ''
  sel.value = 0
  if (!files.loaded) files.refresh().catch(() => {})
  loadHosts()
  await nextTick()
  input.value?.focus()
}
function close() { open.value = false }
async function choose(it?: Item) {
  if (!it) return
  close()
  try { await it.run() } catch (e) { say(e instanceof Error ? e.message : String(e)) }
}
function onKeyList(e: KeyboardEvent) {
  if (e.key === 'ArrowDown') { sel.value = Math.min(sel.value + 1, results.value.length - 1); e.preventDefault(); scrollSel() }
  else if (e.key === 'ArrowUp') { sel.value = Math.max(sel.value - 1, 0); e.preventDefault(); scrollSel() }
  else if (e.key === 'Enter') { choose(results.value[sel.value]); e.preventDefault() }
  else if (e.key === 'Escape') { close(); e.preventDefault() }
}
function scrollSel() { nextTick(() => document.querySelector('.pal .it.on')?.scrollIntoView({ block: 'nearest' })) }

function typing(e: KeyboardEvent): boolean {
  const el = e.target as HTMLElement | null
  return !!el && (el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName) || !!el.closest('.cm-editor'))
}
let pendingG = 0
function onKey(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    open.value ? close() : show()
    return
  }
  if (open.value || typing(e) || e.metaKey || e.ctrlKey || e.altKey) return
  if (e.key === '?') { help.value = !help.value; e.preventDefault(); return }
  if (e.key === 'Escape' && help.value) { help.value = false; return }
  if (e.key === '/') { e.preventDefault(); show(); return }
  if (pendingG && Date.now() - pendingG < 1200) {
    pendingG = 0
    const s = SCREENS.value.find((x) => x.key === e.key)
    if (s) { e.preventDefault(); go(s.to) }
    return
  }
  if (e.key === 'g') pendingG = Date.now()
}
onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
defineExpose({ show })
// other components open it via a window event (the header search box)
onMounted(() => window.addEventListener('gw:palette', show))
onBeforeUnmount(() => window.removeEventListener('gw:palette', show))

// grouped by kind when browsing; one list in score order when searching
const grouped = computed(() => {
  const g: { group: string; items: { it: Item; idx: number }[] }[] = []
  results.value.forEach((it, idx) => {
    const name = q.value ? '' : it.group
    if (!g.length || g[g.length - 1].group !== name) g.push({ group: name, items: [] })
    g[g.length - 1].items.push({ it, idx })
  })
  return g
})
</script>

<template>
  <div v-if="open" class="pal-back" @mousedown.self="close">
    <div class="pal" role="dialog" aria-modal="true" aria-label="Command palette">
      <input ref="input" v-model="q" class="pin" type="text" placeholder="Jump to a file, run, host, environment or action…" aria-label="Search commands"
        role="combobox" aria-expanded="true" aria-controls="pal-list" :aria-activedescendant="results[sel] ? 'pal-' + sel : undefined" @keydown="onKeyList" />
      <ul id="pal-list" class="list" role="listbox" aria-label="Results">
        <template v-for="g in grouped" :key="g.group + g.items[0]?.idx">
          <li v-if="g.group" class="grp lbl" role="presentation">{{ g.group }}</li>
          <li v-for="x in g.items" :id="'pal-' + x.idx" :key="x.it.id" class="it" :class="{ on: x.idx === sel }" role="option" :aria-selected="x.idx === sel"
            @mousemove="sel = x.idx" @click="choose(x.it)">
            <span class="mono lab">{{ x.it.label }}</span><span v-if="x.it.hint" class="mono hint">{{ x.it.hint }}</span>
          </li>
        </template>
        <li v-if="!results.length" class="empty">No matches.</li>
      </ul>
      <div class="foot mono"><span><kbd>↑</kbd><kbd>↓</kbd> move</span><span><kbd>↵</kbd> open</span><span><kbd>esc</kbd> close</span></div>
    </div>
  </div>
  <div v-if="help" class="pal-back" @mousedown.self="help = false">
    <div class="pal helpbox" role="dialog" aria-modal="true" aria-labelledby="kbd-title" @keydown.esc="help = false">
      <h2 id="kbd-title">Keyboard shortcuts</h2>
      <dl class="keys">
        <dt><kbd>⌘</kbd><kbd>K</kbd> or <kbd>/</kbd></dt><dd>Jump anywhere</dd>
        <template v-for="s in SCREENS" :key="s.key"><dt><kbd>g</kbd> <kbd>{{ s.key }}</kbd></dt><dd>{{ s.label }}</dd></template>
        <dt><kbd>⌘</kbd><kbd>S</kbd></dt><dd>Save the open file (Code)</dd>
        <dt><kbd>?</kbd></dt><dd>This list</dd>
      </dl>
      <button class="btn sm" type="button" @click="help = false">Close</button>
    </div>
  </div>
  <p v-if="notice" class="toast" role="status">{{ notice }}</p>
</template>

<style scoped>
.pal-back { position: fixed; inset: 0; z-index: 100; background: rgba(5, 7, 8, .6); display: flex; justify-content: center; align-items: flex-start; padding: 12vh 16px 16px; }
.pal { width: min(640px, 100%); max-height: 70vh; display: flex; flex-direction: column; background: #12161A; border: 1px solid var(--line-strong); border-radius: 12px; box-shadow: 0 24px 64px rgba(0, 0, 0, .6); overflow: hidden; }
.pin { height: 48px; padding: 0 16px; background: transparent; border: 0; border-bottom: 1px solid var(--line); color: var(--text); font: inherit; font-size: 15px; outline: none; }
.list { list-style: none; margin: 0; padding: 6px; overflow: auto; }
.grp { padding: 10px 10px 4px; }
.it { display: flex; align-items: center; gap: 12px; padding: 8px 10px; border-radius: 6px; cursor: pointer; font-size: 13px; }
.it.on { background: #1C2226; box-shadow: inset 2px 0 0 var(--accent); }
.lab { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hint { color: var(--text-label); font-size: 11.5px; white-space: nowrap; }
.empty { padding: 16px; color: var(--text-muted); font-size: 13px; }
.foot { display: flex; gap: 16px; padding: 8px 14px; border-top: 1px solid var(--line); font-size: 11px; color: var(--text-label); }
kbd { font: 500 11px var(--font-mono); border: 1px solid var(--line-strong); border-radius: 4px; padding: 1px 5px; margin-right: 2px; color: var(--text-muted); }
.helpbox { padding: 20px; gap: 14px; width: min(420px, 100%); }
.helpbox h2 { margin: 0; font-size: 16px; }
.keys { display: grid; grid-template-columns: auto 1fr; gap: 8px 16px; margin: 0; font-size: 13px; }
.keys dd { margin: 0; color: var(--text-muted); }
.helpbox .btn { align-self: flex-end; }
.toast { position: fixed; bottom: 16px; left: 50%; transform: translateX(-50%); z-index: 101; margin: 0; padding: 10px 16px; border-radius: 8px; background: #1C2226; border: 1px solid var(--line-strong); font-size: 13px; }
</style>
