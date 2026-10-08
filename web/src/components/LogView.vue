<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import type { LogLine } from '../api/types'

const props = defineProps<{ lines: LogLine[]; follow: boolean; wrap: boolean }>()
const emit = defineEmits<{ (e: 'update:follow', v: boolean): void }>()

const ROW = 21
const OVERSCAN = 30
const box = ref<HTMLDivElement>()
const top = ref(0)
const height = ref(600)

interface Row { key: string; t: string; stage: string; level: string; text: string; first: boolean }

const hms = (iso: string) => new Date(iso).toLocaleTimeString([], { hour12: false })

// A log line may hold several visual lines (diagnostics); virtualisation needs fixed-height rows.
const rows = computed<Row[]>(() => {
  const out: Row[] = []
  for (const l of props.lines) {
    const parts = props.wrap ? [l.text] : l.text.split('\n')
    parts.forEach((p, i) => out.push({ key: l.n + ':' + i, t: i ? '' : hms(l.t), stage: i ? '' : l.stage, level: l.level, text: p, first: i === 0 }))
  }
  return out
})

const WRAP_LIMIT = 3000
const visible = computed(() => {
  if (props.wrap) return { start: 0, items: rows.value.slice(-WRAP_LIMIT), pad: 0, total: 0 }
  const start = Math.max(0, Math.floor(top.value / ROW) - OVERSCAN)
  const end = Math.min(rows.value.length, Math.ceil((top.value + height.value) / ROW) + OVERSCAN)
  return { start, items: rows.value.slice(start, end), pad: start * ROW, total: rows.value.length * ROW }
})

function onScroll() {
  const el = box.value!
  top.value = el.scrollTop
  const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 8
  if (!atBottom && props.follow) emit('update:follow', false)
}

function toBottom() {
  const el = box.value
  if (el) el.scrollTop = el.scrollHeight
}

watch(() => rows.value.length, async () => { if (props.follow) { await nextTick(); toBottom() } })
watch(() => props.follow, async (f) => { if (f) { await nextTick(); toBottom() } })
onMounted(() => {
  const el = box.value!
  height.value = el.clientHeight
  new ResizeObserver(() => (height.value = el.clientHeight)).observe(el)
  if (props.follow) toBottom()
})
</script>

<template>
  <div ref="box" class="log" role="log" aria-live="off" aria-label="Log output" tabindex="0" @scroll="onScroll">
    <div class="spacer" :style="wrap ? undefined : { height: visible.total + 'px' }">
      <div :style="wrap ? undefined : { transform: `translateY(${visible.pad}px)` }">
        <div v-for="r in visible.items" :key="r.key" class="lg" :class="[r.level, { wrap }]">
          <span class="t">{{ r.t }}</span><span class="s">{{ r.stage }}</span><span class="m">{{ r.text }}</span>
        </div>
      </div>
    </div>
    <p v-if="!rows.length" class="empty muted">No log output.</p>
  </div>
</template>

<style scoped>
.log { height: 100%; overflow: auto; padding: 10px 0; background: var(--bg-inset); }
.spacer { position: relative; min-width: 560px; }
.lg { display: grid; grid-template-columns: 78px 68px minmax(0, 1fr); gap: 12px; padding: 0 16px; font: 13.5px/21px var(--font-mono); font-variant-ligatures: none; min-height: 21px; }
.lg:hover { background: var(--bg-raised); }
.t { color: var(--text-faint); } .s { color: var(--text-faint); }
.m { white-space: pre; }
.lg.wrap .m { white-space: pre-wrap; overflow-wrap: anywhere; }
.warn .m { color: var(--warn); } .error .m { color: var(--fail-strong); } .debug .m { color: var(--text-faint); }
.empty { margin: 0; padding: 16px; }
</style>
