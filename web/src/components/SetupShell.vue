<script setup lang="ts">
import { computed } from 'vue'
import { useWorkspace } from '../stores/workspace'
import ThemeToggle from './ThemeToggle.vue'

defineProps<{ title: string }>()
const store = useWorkspace()
const meta = computed(() => {
  const w = store.ws
  if (!w) return ''
  return `${w.root} · ${w.git ? 'git repo' : 'not a git repo'}`
})
</script>

<template>
  <div class="shell">
    <header class="top">
      <div class="logo" aria-hidden="true">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round">
          <path d="M3 20h18" /><path d="M5 16h14" /><path d="M8 12h8" /><path d="M11 8h2" />
        </svg>
      </div>
      <a class="mono name" href="/" title="All projects">groundwork</a>
      <span class="sep">/</span>
      <span class="muted">{{ title }}</span>
      <span class="mono meta">{{ meta }}</span>
      <ThemeToggle />
    </header>
    <main class="body"><slot /></main>
  </div>
</template>

<style scoped>
.shell { min-height: 100vh; }
.top { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 16px 32px; border-bottom: 1px solid var(--line); }
.logo { flex: none; width: 28px; height: 28px; border-radius: 6px; background: var(--accent); color: var(--on-accent); display: grid; place-items: center; }
.name { font-weight: 700; color: var(--text); text-decoration: none; }
.name:hover { color: var(--accent-hover); }
.sep { color: var(--text-faint); }
.meta { margin-left: auto; font-size: 13px; color: var(--text-label); word-break: break-all; }
.body { max-width: 1280px; margin: 0 auto; padding: 32px; display: flex; flex-wrap: wrap; gap: 28px; align-items: flex-start; }
@media (max-width: 640px) { .body { padding: 20px 16px; } .top { padding: 14px 16px; } }
</style>
