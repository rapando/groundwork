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
.st-ok { color: var(--ok); background: color-mix(in srgb, var(--ok) 10%, transparent); }
.st-warn { color: var(--warn); background: color-mix(in srgb, var(--warn) 10%, transparent); }
.st-fail { color: var(--fail); background: color-mix(in srgb, var(--fail) 10%, transparent); }
.st-run { color: var(--running); background: color-mix(in srgb, var(--running) 12%, transparent); }
.st-idle { color: var(--text-muted); background: color-mix(in srgb, var(--text-muted) 10%, transparent); }
</style>
