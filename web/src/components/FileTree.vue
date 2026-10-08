<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import TreeNode, { type Node } from './TreeNode.vue'
import type { Section } from '../api/types'
import type { Badge } from '../stores/checks'

const props = defineProps<{
  files: string[]
  sections: Section[]
  selected: string
  badges: Map<string, Badge>
  git: Map<string, { status: string }>
  iacOnly: boolean
}>()
const emit = defineEmits<{ (e: 'open', path: string): void; (e: 'iac', v: boolean): void }>()

const filter = ref('')
const expanded = ref(new Set<string>(loadExpanded()))

function loadExpanded(): string[] {
  try { return JSON.parse(localStorage.getItem('gw.expanded') ?? '[]') } catch { return [] }
}
function persist() {
  try { localStorage.setItem('gw.expanded', JSON.stringify([...expanded.value])) } catch { /* ignore */ }
}

function build(paths: string[], base: string): Node[] {
  const root: Node = { name: '', path: base, dir: true, children: [] }
  for (const p of paths) {
    const rel = base === '.' ? p : p.slice(base.length + 1)
    const parts = rel.split('/')
    let cur = root
    parts.forEach((part, i) => {
      const full = (base === '.' ? '' : base + '/') + parts.slice(0, i + 1).join('/')
      let child = cur.children.find((c) => c.name === part && c.dir === (i < parts.length - 1))
      if (!child) {
        child = { name: part, path: full, dir: i < parts.length - 1, children: [] }
        cur.children.push(child)
      }
      cur = child
    })
  }
  const sort = (n: Node) => {
    n.children.sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1))
    n.children.forEach(sort)
  }
  sort(root)
  return root.children
}

const visible = computed(() => {
  const q = filter.value.trim().toLowerCase()
  return q ? props.files.filter((f) => f.toLowerCase().includes(q)) : props.files
})

const groups = computed(() => {
  const claimed = new Set<string>()
  const out: { title: string; nodes: Node[] }[] = []
  for (const s of props.sections) {
    const inSec = visible.value.filter((f) => s.base === '.' || f === s.base || f.startsWith(s.base + '/'))
    inSec.forEach((f) => claimed.add(f))
    if (inSec.length) out.push({ title: s.name, nodes: build(inSec, s.base) })
  }
  const rest = visible.value.filter((f) => !claimed.has(f))
  if (rest.length) out.push({ title: props.sections.length ? 'Other files' : 'Files', nodes: build(rest, '.') })
  return out
})

// Default: open each section's first two levels; always open the path to the selected file.
function seed() {
  if (expanded.value.size === 0) {
    for (const s of props.sections) {
      const prefix = s.base === '.' ? '' : s.base + '/'
      for (const f of props.files) {
        if (!(s.base === '.' || f.startsWith(prefix))) continue
        const rel = f.slice(prefix.length).split('/')
        for (let i = 1; i <= Math.min(rel.length - 1, 2); i++) expanded.value.add(prefix + rel.slice(0, i).join('/'))
      }
    }
  }
}
function revealSelected() {
  const parts = props.selected.split('/')
  for (let i = 1; i < parts.length; i++) expanded.value.add(parts.slice(0, i).join('/'))
}
watch(() => props.files, seed, { immediate: true })
watch(() => props.selected, () => { revealSelected(); persist() }, { immediate: true })

const openAll = computed(() => filter.value.trim() !== '')
const effective = computed(() => {
  if (!openAll.value) return expanded.value
  const all = new Set<string>()
  for (const f of visible.value) {
    const parts = f.split('/')
    for (let i = 1; i < parts.length; i++) all.add(parts.slice(0, i).join('/'))
  }
  return all
})

function toggle(p: string) {
  if (expanded.value.has(p)) expanded.value.delete(p)
  else expanded.value.add(p)
  expanded.value = new Set(expanded.value)
  persist()
}
</script>

<template>
  <section class="files" aria-label="Repository files">
    <label class="search">
      <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><circle cx="11" cy="11" r="7" /><path d="M20 20l-4-4" /></svg>
      <input v-model="filter" type="search" placeholder="Filter files" aria-label="Filter files" />
    </label>
    <div class="seg-group" role="group" aria-label="Files shown">
      <button class="seg" :class="{ on: iacOnly }" type="button" :aria-pressed="iacOnly" @click="emit('iac', true)">IaC only</button>
      <button class="seg" :class="{ on: !iacOnly }" type="button" :aria-pressed="!iacOnly" @click="emit('iac', false)">All files</button>
    </div>
    <div role="tree" class="tree-root">
      <template v-for="g in groups" :key="g.title">
        <span class="lbl sec">{{ g.title }}</span>
        <TreeNode
          v-for="n in g.nodes" :key="n.path" :node="n" :depth="0" :selected="selected" :expanded="effective"
          :badges="badges" :git="git" @toggle="toggle" @open="emit('open', $event)"
        />
      </template>
      <p v-if="!groups.length" class="muted empty">{{ filter ? 'No files match.' : 'No files.' }}</p>
    </div>
  </section>
</template>

<style scoped>
.files { display: flex; flex-direction: column; gap: 2px; padding: 12px 8px; background: #0F1315; overflow: auto; min-height: 0; }
.search { display: flex; align-items: center; gap: 8px; height: 32px; margin: 0 4px 8px; padding: 0 10px; border: 1px solid var(--line-strong); border-radius: 6px; color: var(--text-label); }
.search input { flex: 1; min-width: 0; background: transparent; border: 0; outline: none; color: var(--text); font-size: 13px; }
.seg-group { display: flex; gap: 2px; padding: 2px; margin: 0 4px 8px; border: 1px solid var(--line-strong); border-radius: 7px; background: #111518; }
.seg { flex: 1; font-size: 13px; height: 26px; border: 0; border-radius: 5px; background: transparent; color: var(--text-muted); cursor: pointer; }
.seg.on { background: var(--line-strong); color: var(--text); }
.sec { padding: 10px 10px 6px; display: block; }
.empty { padding: 8px 10px; font-size: 13px; }
</style>
