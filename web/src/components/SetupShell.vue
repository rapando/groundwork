<script setup lang="ts">
import { computed } from 'vue'
import { useWorkspace } from '../stores/workspace'
import BrandMark from './BrandMark.vue'
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
      <BrandMark />
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
.name { font-weight: 700; color: var(--text); text-decoration: none; }
.name:hover { color: var(--accent-hover); }
.sep { color: var(--text-faint); }
.meta { margin-left: auto; font-size: 13px; color: var(--text-label); word-break: break-all; }
.body { max-width: 1280px; margin: 0 auto; padding: 32px; display: flex; flex-wrap: wrap; gap: 28px; align-items: flex-start; }
@media (max-width: 640px) { .body { padding: 20px 16px; } .top { padding: 14px 16px; } }
</style>
