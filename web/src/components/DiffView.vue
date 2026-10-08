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
.diff { height: 100%; overflow: auto; background: #0D1012; font-size: 13.5px; line-height: 20px; padding: 12px 0; }
.l { white-space: pre; padding: 0 16px; }
.add { background: rgba(95, 211, 141, .10); color: var(--ok); }
.del { background: rgba(255, 122, 122, .10); color: var(--fail); }
.hunk { color: var(--running); }
.meta { color: var(--text-label); }
.pad { padding: 12px 16px; margin: 0; }
</style>
