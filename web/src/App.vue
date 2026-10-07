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

const ws = useWorkspace()
const checks = useChecks()
const files = useFiles()
const git = useGit()
const runs = useRuns()
const issues = useIssues()

onMounted(() => {
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
  <RouterView />
  <CommandPalette />
</template>
