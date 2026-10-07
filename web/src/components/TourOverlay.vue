<script setup lang="ts">
// First-run tips. Each step points at a [data-tour=…] anchor; floating-ui
// keeps the tip next to it at any window size.
import { computePosition, flip, offset, shift } from '@floating-ui/dom'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

export interface TourStep { title: string; body: string; anchor?: string; keys?: boolean }
const props = defineProps<{ steps: TourStep[] }>()
const emit = defineEmits<{ done: [] }>()

const i = ref(0)
const tip = ref<HTMLElement | null>(null)
const style = ref<Record<string, string>>({ top: '96px', right: '28px' })
const ring = ref<Record<string, string> | null>(null)
const step = computed(() => props.steps[Math.min(i.value, props.steps.length - 1)])
const last = computed(() => i.value === props.steps.length - 1)

// The highlight is a separate ring drawn over the anchor's box, so the app's
// own elements are never modified (and re-renders can't drop it).
const anchor = () => (step.value.anchor ? document.querySelector<HTMLElement>(`[data-tour="${step.value.anchor}"]`) : null)
let seq = 0
async function place() {
  const my = ++seq
  await nextTick()
  const el = anchor()
  if (!tip.value || my !== seq) return
  if (!el) { ring.value = null; style.value = { top: '96px', right: '28px' }; return }
  const r = el.getBoundingClientRect()
  ring.value = { left: `${r.left - 6}px`, top: `${r.top - 6}px`, width: `${r.width + 12}px`, height: `${r.height + 12}px` }
  const { x, y } = await computePosition(el, tip.value, { placement: 'right-start', strategy: 'fixed', middleware: [offset(14), flip({ fallbackPlacements: ['bottom-start', 'top-start', 'left-start'] }), shift({ padding: 16 })] })
  if (my === seq) style.value = { left: `${x}px`, top: `${y}px` }
}
watch(i, () => { anchor()?.scrollIntoView({ block: 'nearest' }); place() })
let raf = 0
const onMove = () => { cancelAnimationFrame(raf); raf = requestAnimationFrame(place) }
// the page keeps loading under the tour (environments, the checklist), so
// anchors can appear or move after a step starts: keep following them
let follow = 0
onMounted(() => {
  place()
  follow = window.setInterval(onMove, 400)
  window.addEventListener('resize', onMove)
  window.addEventListener('scroll', onMove, true)
  tip.value?.querySelector<HTMLButtonElement>('.primary')?.focus()
})
onBeforeUnmount(() => { window.clearInterval(follow); window.removeEventListener('resize', onMove); window.removeEventListener('scroll', onMove, true); cancelAnimationFrame(raf) })
function finish() { emit('done') }
</script>

<template>
  <div class="tour-dim" aria-hidden="true" />
  <div v-if="ring" class="tour-ring" aria-hidden="true" :data-anchor="step.anchor" :style="ring" />
  <div ref="tip" class="tip" role="dialog" aria-label="Tips" :style="style" @keydown.esc="finish">
    <div class="th">
      <span class="mono tn">Tip {{ i + 1 }} of {{ steps.length }}</span>
      <button type="button" class="x" aria-label="Close tips" @click="finish">×</button>
    </div>
    <h2>{{ step.title }}</h2>
    <p>{{ step.body }}</p>
    <div v-if="step.keys" class="keys">
      <span><kbd>⌘K</kbd> jump anywhere</span><span><kbd>?</kbd> all shortcuts</span><span><kbd>g</kbd> <kbd>r</kbd> runs</span>
    </div>
    <div class="tf">
      <span class="dots"><span v-for="(_, k) in steps" :key="k" class="dot" :class="{ on: k === i }" /></span>
      <span class="btns">
        <button v-if="i > 0" class="btn sm" type="button" @click="i--">Back</button>
        <button class="btn sm primary" type="button" @click="last ? finish() : i++">{{ last ? 'Done' : i === 0 ? 'Show me around' : 'Next' }}</button>
      </span>
    </div>
    <button v-if="i === 0" type="button" class="skip" @click="finish">Skip tour. You can reopen it from the ? button</button>
  </div>
</template>

<style scoped>
.tour-dim { position: fixed; inset: 0; z-index: 40; background: rgba(5, 7, 8, .62); pointer-events: none; }
.tour-ring { position: fixed; z-index: 45; border: 2px solid var(--accent); border-radius: 12px; box-shadow: 0 0 0 9999px rgba(5, 7, 8, .0); pointer-events: none; transition: all .15s ease; }
.tip { position: fixed; z-index: 60; width: 360px; max-width: calc(100% - 32px); box-sizing: border-box; padding: 18px 20px; display: flex; flex-direction: column; gap: 12px; border: 1px solid #3A4A2A; border-radius: 12px; background: #151A13; box-shadow: 0 18px 48px rgba(0, 0, 0, .55); }
.th { display: flex; align-items: center; justify-content: space-between; }
.tn { font-size: 11px; color: var(--accent); text-transform: uppercase; letter-spacing: .08em; }
.x { width: 28px; height: 28px; border: 0; border-radius: 6px; background: transparent; color: var(--text-muted); cursor: pointer; font-size: 16px; }
h2 { margin: 0; font-size: 17px; font-weight: 600; line-height: 1.3; }
p { margin: 0; font-size: 13.5px; color: #C3CBD0; line-height: 1.55; }
.keys { display: flex; flex-wrap: wrap; gap: 8px 14px; font-size: 12px; color: var(--text-muted); }
kbd { font: 500 11px var(--font-mono); border: 1px solid var(--line-strong); border-radius: 4px; padding: 1px 5px; }
.tf { display: flex; align-items: center; gap: 10px; padding-top: 4px; }
.dots { display: flex; gap: 5px; }
.dot { width: 6px; height: 6px; border-radius: 50%; background: #3A4450; } .dot.on { background: var(--accent); }
.btns { margin-left: auto; display: flex; gap: 8px; }
.skip { align-self: flex-start; border: 0; background: transparent; color: var(--text-label); font: inherit; font-size: 12px; padding: 0; cursor: pointer; text-decoration: underline; }
</style>
