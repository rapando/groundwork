<script setup lang="ts">
import { onMounted } from 'vue'
import { RouterView } from 'vue-router'
import { onEvent, startEvents } from './composables/events'
import { useChecks } from './stores/checks'
import { useFiles } from './stores/files'
import { useGit } from './stores/git'
import { useRuns } from './stores/runs'
import { useWorkspace } from './stores/workspace'
import { useIssues } from './stores/issues'
import CommandPalette from './components/CommandPalette.vue'
import { projectId } from './project'

const ws = useWorkspace()
const checks = useChecks()
const files = useFiles()
const git = useGit()
const runs = useRuns()
const issues = useIssues()

onMounted(() => {
  if (!projectId) return // the project list has no per-project stores or stream
  // one SSE stream feeds every store; they refetch snapshots after a reconnect
  checks.init()
  files.init()
  git.init()
  runs.init()
  runs.refresh().catch(() => {})
  issues.listen()
  issues.refresh().catch(() => {})
  onEvent('workspace.updated', () => ws.refresh())
  onEvent('reconnected', () => ws.refresh())
  startEvents()
})
</script>

<template>
  <div v-if="projectId && ws.error && !ws.ws" class="gone" role="alert">
    <h1>This project isn't available</h1>
    <p>{{ ws.error }}</p>
    <a class="btn primary" href="/">All projects</a>
  </div>
  <RouterView v-else />
  <CommandPalette v-if="projectId" />
</template>

<style scoped>
.gone { max-width: 560px; margin: 15vh auto 0; padding: 0 16px; display: flex; flex-direction: column; align-items: flex-start; gap: 12px; }
.gone h1 { margin: 0; font-size: 24px; }
.gone p { margin: 0; color: var(--text-muted); font-size: 15px; }
</style>
