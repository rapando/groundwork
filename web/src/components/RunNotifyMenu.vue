<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import DropMenu from './DropMenu.vue'
import { api } from '../api/client'
import type { Channel, NotifyMode, NotifySettings, NotifySettingsResponse } from '../api/types'

/** Whether to hear back about one run, overriding the project's notify settings for it. */
const props = defineProps<{ runId: number }>()
const mode = ref<NotifyMode>('default')
const error = ref('')
const settings = ref<NotifySettings | null>(null)

const OPTIONS: { mode: Exclude<NotifyMode, 'default'>; label: string; hint: string }[] = [
  { mode: 'finish', label: 'When it finishes', hint: 'Tell me whether it succeeds or fails.' },
  { mode: 'fail', label: 'Only if it fails', hint: 'Stay quiet if it succeeds.' },
  { mode: 'none', label: "Don't notify", hint: 'Keep it in the list only.' },
]
const label = (m: NotifyMode) => OPTIONS.find((o) => o.mode === m)?.label ?? ''

// what the project default does for a run: from the notify settings
const effective = computed<Exclude<NotifyMode, 'default'>>(() => {
  const s = settings.value
  if (!s) return 'fail'
  return s.success?.length ? 'finish' : s.failed?.length ? 'fail' : 'none'
})
const names: Record<Channel, string> = { console: 'console', desktop: 'desktop', webhook: 'webhook' }
const list = (c?: Channel[]) => (c?.length ? c.map((x) => names[x] ?? x).join(', ') : 'nowhere')
const defaultHint = computed(() => {
  const s = settings.value
  if (!s) return 'As set in groundwork.yaml.'
  return `Failures: ${list(s.failed)}. Successes: ${list(s.success)}${s.success?.length && s.quiet_under ? ` (over ${s.quiet_under})` : ''}.`
})

async function load() {
  error.value = ''
  try { mode.value = (await api<{ mode: NotifyMode }>(`/runs/${props.runId}/notify`)).mode ?? 'default' } catch { mode.value = 'default' }
}
watch(() => props.runId, load, { immediate: true })
api<NotifySettingsResponse>('/notifications/settings').then((r) => (settings.value = r.settings)).catch(() => {})

async function choose(m: NotifyMode) {
  const was = mode.value
  mode.value = m
  error.value = ''
  try { mode.value = (await api<{ mode: NotifyMode }>(`/runs/${props.runId}/notify`, { method: 'PUT', body: { mode: m } })).mode }
  catch (e) { mode.value = was; error.value = e instanceof Error ? e.message : String(e) }
}
</script>

<template>
  <div class="nm">
    <DropMenu label="Notify me about this run" align="end">
      <template #button>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 8a6 6 0 1 1 12 0c0 7 3 9 3 9H3s3-2 3-9" /><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0" /></svg>
        <span>{{ label(mode === 'default' ? effective : mode) }}</span>
        <span v-if="mode === 'default'" class="muted dflt">· default</span>
        <span aria-hidden="true" class="caret">▾</span>
      </template>
      <button v-for="o in OPTIONS" :key="o.mode" role="menuitemradio" type="button" :aria-checked="mode === o.mode" @click="choose(o.mode)">
        <span>{{ o.label }}</span><span class="hint">{{ o.hint }}</span>
      </button>
      <div class="sep" role="separator" />
      <button role="menuitemradio" type="button" :aria-checked="mode === 'default'" @click="choose('default')">
        <span>Project default</span><span class="hint">{{ defaultHint }}</span>
      </button>
    </DropMenu>
    <p v-if="error" class="err" role="alert">{{ error }}</p>
  </div>
</template>

<style scoped>
.nm { display: inline-flex; flex-direction: column; align-items: flex-end; gap: 4px; }
.nm :deep(.btn) { gap: 6px; }
.dflt { font-size: 13px; }
.caret { font-size: 11px; color: var(--text-muted); }
.hint { font-size: 13px; color: var(--text-muted); white-space: normal; }
.err { margin: 0; }
</style>
