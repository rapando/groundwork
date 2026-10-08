<script setup lang="ts">
import { theme } from '../composables/theme'

const OPTIONS = [
  { value: 'paper', label: 'Paper' },
  { value: 'dark', label: 'Dark' },
] as const
</script>

<template>
  <div class="theme" role="radiogroup" aria-label="Theme">
    <button v-for="o in OPTIONS" :key="o.value" type="button" role="radio" class="opt-t" :class="{ on: theme === o.value }"
      :aria-checked="theme === o.value" @click="theme = o.value">
      <svg v-if="o.value === 'paper'" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" /></svg>
      <svg v-else width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 14.5A8 8 0 0 1 9.5 4 8 8 0 1 0 20 14.5z" /></svg>
      {{ o.label }}
    </button>
  </div>
</template>

<style scoped>
.theme { display: inline-flex; gap: 2px; padding: 2px; border: 1px solid var(--line-strong); border-radius: 7px; background: var(--bg-card); }
.opt-t { display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 10px; border: 0; border-radius: 5px; background: transparent; color: var(--text-muted); font: inherit; font-size: 13px; cursor: pointer; }
.opt-t:hover { color: var(--text); }
.opt-t.on { background: var(--bg-active); color: var(--text); box-shadow: inset 0 0 0 1px var(--line-strong); }
</style>
