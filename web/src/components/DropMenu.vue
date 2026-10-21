<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref } from 'vue'

/**
 * A button that opens a menu below it. Put buttons with role="menuitem" (or
 * menuitemradio) in the default slot; the arrow keys move between them,
 * Escape closes the menu and returns focus to the button, and choosing one or
 * clicking outside closes it.
 */
defineProps<{ label?: string; align?: 'start' | 'end' }>()
const open = ref(false)
const root = ref<HTMLElement>()
const btn = ref<HTMLButtonElement>()
const items = () => [...(root.value?.querySelectorAll<HTMLElement>('[role^="menuitem"]:not([disabled])') ?? [])]

function outside(e: PointerEvent) { if (!root.value?.contains(e.target as Node)) close(false) }
async function show(focus: 'first' | 'last' | 'checked' = 'checked') {
  open.value = true
  document.addEventListener('pointerdown', outside)
  await nextTick()
  const list = items()
  const pick = focus === 'last' ? list[list.length - 1] : focus === 'checked' ? list.find((el) => el.getAttribute('aria-checked') === 'true') ?? list[0] : list[0]
  pick?.focus()
}
function close(refocus = true) {
  if (!open.value) return
  open.value = false
  document.removeEventListener('pointerdown', outside)
  if (refocus) btn.value?.focus()
}
onBeforeUnmount(() => document.removeEventListener('pointerdown', outside))

function onButtonKey(e: KeyboardEvent) {
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); show(e.key === 'ArrowUp' ? 'last' : 'first') }
}
function onMenuKey(e: KeyboardEvent) {
  const list = items()
  const i = list.indexOf(document.activeElement as HTMLElement)
  const go = (n: number) => { e.preventDefault(); list[(n + list.length) % list.length]?.focus() }
  if (e.key === 'ArrowDown') go(i + 1)
  else if (e.key === 'ArrowUp') go(i - 1)
  else if (e.key === 'Home') go(0)
  else if (e.key === 'End') go(list.length - 1)
  else if (e.key === 'Escape') { e.preventDefault(); close() }
  else if (e.key === 'Tab') close(false)
}
// a chosen item closes the menu (its own click handler runs first)
function onMenuClick(e: MouseEvent) { if ((e.target as HTMLElement).closest('[role^="menuitem"]')) close() }
defineExpose({ close })
</script>

<template>
  <div ref="root" class="dm">
    <button ref="btn" class="btn sm" type="button" aria-haspopup="menu" :aria-expanded="open" :aria-label="label" @click="open ? close() : show()" @keydown="onButtonKey">
      <slot name="button" />
    </button>
    <div v-if="open" class="menu" :class="align === 'start' ? 'start' : 'end'" role="menu" :aria-label="label" @keydown="onMenuKey" @click="onMenuClick">
      <slot />
    </div>
  </div>
</template>

<style scoped>
.dm { position: relative; display: inline-flex; }
.menu { position: absolute; top: calc(100% + 6px); z-index: 20; min-width: 220px; max-width: min(320px, calc(100vw - 32px)); padding: 6px; display: flex; flex-direction: column; gap: 2px;
  background: var(--bg-card); border: 1px solid var(--line-strong); border-radius: 8px; box-shadow: var(--shadow); }
.menu.end { right: 0; } .menu.start { left: 0; }
.menu :slotted([role^='menuitem']) { all: unset; box-sizing: border-box; display: flex; flex-direction: column; gap: 2px; padding: 8px 10px 8px 28px; border-radius: 6px; font-size: 14px; color: var(--text); cursor: pointer; position: relative; }
.menu :slotted([role^='menuitem']:hover), .menu :slotted([role^='menuitem']:focus-visible) { background: var(--bg-hover); outline: none; }
.menu :slotted([role^='menuitem']:focus-visible) { box-shadow: inset 0 0 0 2px var(--accent); }
.menu :slotted([role^='menuitem'][disabled]) { color: var(--text-disabled); cursor: default; background: none; }
.menu :slotted([aria-checked='true'])::before { content: '✓'; position: absolute; left: 10px; top: 8px; color: var(--accent); font-weight: 600; }
.menu :slotted(.sep) { height: 1px; margin: 4px 2px; background: var(--line); }
</style>
