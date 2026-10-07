<script setup lang="ts">
import { computed } from 'vue'
import type { RunStage } from '../api/types'
import { duration } from '../stores/runs'
import { useNow } from '../composables/now'

const props = defineProps<{ stages: RunStage[]; plan?: { create: number; update: number; replace: number; delete: number } }>()
const now = useNow()

// "summary" (reading the plan back) is plumbing: only show it if it breaks
const items = computed(() => props.stages.filter((s) => s.name !== 'summary' || s.status === 'failed').map((s) => {
  let note = ''
  switch (s.status) {
    case 'succeeded': note = '✓ ' + duration(s.started_at, s.ended_at); break
    case 'running': note = (s.name === 'approve' ? '● waiting' : '● ' + duration(s.started_at, undefined, now.value)); break
    case 'failed': note = '✕ failed'; break
    case 'cancelled': note = 'cancelled'; break
    case 'skipped': note = 'skipped'; break
    default: note = 'queued'
  }
  if (s.name === 'plan' && s.status === 'succeeded' && props.plan) {
    const p = props.plan
    note += ` · +${p.create + p.replace} ~${p.update} -${p.delete + p.replace}`
  }
  if (s.name === 'approve' && s.status === 'succeeded' && s.detail) note = '✓ ' + s.detail.replace('approved by ', '')
  return { ...s, note: note.trim() }
}))
</script>

<template>
  <ol class="stages" aria-label="Stages" data-tour="stages">
    <template v-for="(s, i) in items" :key="s.name">
      <li v-if="i" class="arrow" aria-hidden="true">›</li>
      <li class="stage" :class="s.status" :title="s.detail">
        <span class="mono n">{{ s.name }}</span>
        <span class="mono note">{{ s.note }}</span>
      </li>
    </template>
  </ol>
</template>

<style scoped>
.stages { list-style: none; margin: 0; padding: 0; display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.arrow { color: #4A545B; }
.stage { display: flex; flex-direction: column; gap: 4px; padding: 10px 14px; border: 1px solid #232A2F; border-radius: 8px; background: var(--bg-panel); min-width: 104px; }
.n { font-size: 12px; }
.note { font-size: 11px; color: var(--text-label); }
.stage.succeeded { border-color: #24402F; } .stage.succeeded .note { color: var(--ok); }
.stage.running { border-color: #2E4D6E; background: #121A22; box-shadow: 0 0 0 3px rgba(124, 192, 255, .08); } .stage.running .note { color: var(--running); }
.stage.failed { border-color: #5A2E2E; } .stage.failed .note { color: var(--fail); }
.stage.pending, .stage.skipped, .stage.cancelled { border-style: dashed; color: var(--text-label); }
</style>
