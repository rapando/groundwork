<script setup lang="ts">
import { STATUS_CLASS, STATUS_LABEL } from '../stores/runs'

defineProps<{ status: string; label?: string }>()
</script>

<template>
  <span class="pill" :class="'st-' + (STATUS_CLASS[status] ?? status)">{{ label ?? STATUS_LABEL[status] ?? status }}</span>
</template>

<style scoped>
.pill { display: inline-flex; align-items: center; gap: 6px; font: 500 12px var(--font-mono); padding: 3px 8px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; white-space: nowrap; }
.pill::before { content: ''; width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.st-ok { color: var(--ok); background: rgba(95, 211, 141, .10); }
.st-warn { color: var(--warn); background: rgba(245, 182, 71, .10); }
.st-fail { color: var(--fail); background: rgba(255, 122, 122, .10); }
.st-run { color: var(--running); background: rgba(124, 192, 255, .12); }
.st-idle { color: var(--text-muted); background: rgba(154, 164, 171, .10); }
</style>
