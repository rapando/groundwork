<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ diff: string }>()
const lines = computed(() => props.diff.split('\n').map((text) => {
  let cls = ''
  if (text.startsWith('+++') || text.startsWith('---') || text.startsWith('diff ') || text.startsWith('index ')) cls = 'meta'
  else if (text.startsWith('@@')) cls = 'hunk'
  else if (text.startsWith('+')) cls = 'add'
  else if (text.startsWith('-')) cls = 'del'
  return { text, cls }
}))
</script>

<template>
  <div class="diff mono" role="region" aria-label="Diff">
    <p v-if="!diff.trim()" class="muted pad">No changes against HEAD.</p>
    <div v-for="(l, i) in lines" :key="i" class="l" :class="l.cls">{{ l.text || ' ' }}</div>
  </div>
</template>

<style scoped>
.diff { height: 100%; overflow: auto; background: var(--bg); font-size: 13.5px; line-height: 20px; padding: 12px 0; }
.l { white-space: pre; padding: 0 16px; }
.add { background: color-mix(in srgb, var(--ok) 10%, transparent); color: var(--ok); }
.del { background: color-mix(in srgb, var(--fail) 10%, transparent); color: var(--fail); }
.hunk { color: var(--running); }
.meta { color: var(--text-label); }
.pad { padding: 12px 16px; margin: 0; }
</style>
