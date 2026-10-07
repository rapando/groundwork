<script setup lang="ts">
import type { Badge } from '../stores/checks'

export interface Node { name: string; path: string; dir: boolean; children: Node[] }

const props = defineProps<{
  node: Node
  depth: number
  selected: string
  expanded: Set<string>
  badges: Map<string, Badge>
  git: Map<string, { status: string }>
}>()
const emit = defineEmits<{ (e: 'toggle', path: string): void; (e: 'open', path: string): void }>()

const gitMark: Record<string, string> = { modified: 'M', added: 'A', untracked: 'U', deleted: 'D', renamed: 'R', conflict: '!' }
const isOpen = () => props.expanded.has(props.node.path)
function click() { props.node.dir ? emit('toggle', props.node.path) : emit('open', props.node.path) }
</script>

<template>
  <div role="none">
    <button
      type="button"
      class="tree mono"
      :class="{ on: !node.dir && selected === node.path }"
      :style="{ paddingLeft: 10 + depth * 16 + 'px' }"
      :role="'treeitem'"
      :aria-expanded="node.dir ? isOpen() : undefined"
      :aria-selected="!node.dir && selected === node.path"
      @click="click"
    >
      <span v-if="node.dir" class="caret" aria-hidden="true">{{ isOpen() ? '▾' : '▸' }}</span>
      <span class="name">{{ node.name }}{{ node.dir ? '/' : '' }}</span>
      <span class="cnt">
        <span v-if="!node.dir && git.get(node.path)" class="gitm" :class="git.get(node.path)!.status" :title="git.get(node.path)!.status">{{ gitMark[git.get(node.path)!.status] ?? 'M' }}</span>
        <template v-if="badges.get(node.path)">
          <span v-if="badges.get(node.path)!.errors" class="fail">{{ badges.get(node.path)!.errors }}{{ node.dir ? '' : ' err' }}</span>
          <span v-else-if="badges.get(node.path)!.warnings" class="warn">{{ badges.get(node.path)!.warnings }}{{ node.dir ? '' : ' warn' }}</span>
        </template>
      </span>
    </button>
    <div v-if="node.dir && isOpen()" role="group">
      <TreeNode
        v-for="c in node.children" :key="c.path" :node="c" :depth="depth + 1" :selected="selected"
        :expanded="expanded" :badges="badges" :git="git" @toggle="emit('toggle', $event)" @open="emit('open', $event)"
      />
    </div>
  </div>
</template>

<style scoped>
.tree {
  all: unset; box-sizing: border-box; width: 100%; display: flex; align-items: center; gap: 8px;
  padding: 5px 10px; border-radius: 4px; color: #C3CBD0; font-size: 12.5px; white-space: nowrap; cursor: pointer;
}
.tree:hover { background: #1A1F23; color: var(--text); }
.tree.on { background: #1F2A1A; color: var(--text); }
.tree:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
.caret { width: 10px; color: var(--text-label); }
.name { overflow: hidden; text-overflow: ellipsis; }
.cnt { margin-left: auto; font-size: 11px; display: inline-flex; gap: 6px; }
.gitm { font-weight: 700; }
.gitm.modified { color: var(--warn); } .gitm.added, .gitm.untracked { color: var(--ok); }
.gitm.deleted, .gitm.conflict { color: var(--fail); } .gitm.renamed { color: var(--running); }
</style>
