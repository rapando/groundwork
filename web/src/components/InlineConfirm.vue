<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'

/**
 * A confirmation built into the page instead of a browser dialog. It takes
 * focus when it appears; Escape or the cancel button dismisses it. Extra
 * content (a typed confirmation, checks) goes in the default slot.
 */
withDefaults(defineProps<{ message: string; confirmLabel: string; cancelLabel?: string; danger?: boolean; disabled?: boolean; busy?: boolean }>(), { cancelLabel: 'Keep it' })
const emit = defineEmits<{ (e: 'confirm'): void; (e: 'cancel'): void }>()
const box = ref<HTMLElement>()
const opener = document.activeElement as HTMLElement | null
// focus a typed confirmation if there is one, else the safe choice, so Enter never confirms by accident
onMounted(() => (box.value?.querySelector<HTMLElement>('input') ?? box.value?.querySelector<HTMLElement>('.keep'))?.focus())
// back to what opened it, if that is still on the page
onBeforeUnmount(() => { if (opener?.isConnected && box.value?.contains(document.activeElement)) opener.focus() })
</script>

<template>
  <div ref="box" class="ic" :class="{ danger }" role="group" :aria-label="message" @keydown.esc.prevent="emit('cancel')">
    <p class="msg">{{ message }}</p>
    <slot />
    <div class="row">
      <button class="btn sm" :class="danger ? 'dg' : 'primary'" type="button" :disabled="disabled || busy" @click="emit('confirm')">{{ confirmLabel }}</button>
      <button class="btn sm keep" type="button" @click="emit('cancel')">{{ cancelLabel }}</button>
    </div>
  </div>
</template>

<style scoped>
.ic { display: flex; flex-direction: column; gap: 10px; padding: 12px 14px; border: 1px solid var(--line-strong); border-radius: 8px; background: var(--bg-raised); }
.ic.danger { border-color: var(--fail-line); background: var(--fail-bg); }
.msg { margin: 0; font-size: 14px; font-weight: 500; color: var(--text); }
.row { display: flex; flex-wrap: wrap; gap: 8px; }
.btn.dg { background: var(--fail); border-color: var(--fail); color: var(--on-accent); }
</style>
