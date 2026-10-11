<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'

/**
 * A modal built on <dialog>.showModal(): the browser gives the focus trap,
 * the inert background, Escape and focus return for free. Use it with v-if
 * and handle `close` by clearing that condition.
 */
defineProps<{ labelledby: string; wide?: boolean }>()
const emit = defineEmits<{ (e: 'close'): void }>()
const el = ref<HTMLDialogElement | null>(null)
const opener = document.activeElement as HTMLElement | null

onMounted(() => {
  const d = el.value
  if (!d) return
  if (typeof d.showModal === 'function') d.showModal()
  else d.setAttribute('open', '')
  const first = d.querySelector<HTMLElement>('input:not([type=hidden]), select, textarea, button:not(.close)')
  first?.focus()
})
onBeforeUnmount(() => {
  el.value?.close()
  opener?.focus?.()
})
// the backdrop is the dialog element itself outside its content box
function backdrop(e: MouseEvent) { if (e.target === el.value) emit('close') }
</script>

<template>
  <dialog ref="el" class="modal" :class="{ wide }" :aria-labelledby="labelledby" @cancel.prevent="emit('close')" @click="backdrop">
    <slot />
  </dialog>
</template>

<style scoped>
.modal { border: 0; padding: 0; background: transparent; color: inherit; width: min(480px, calc(100vw - 32px)); max-width: none; }
.modal.wide { width: min(720px, calc(100vw - 32px)); }
.modal::backdrop { background: var(--scrim); }
.modal :deep(> *) { width: 100%; box-sizing: border-box; }
</style>
