<script setup lang="ts">
// The groundwork mark: a stepped foundation on an accent tile. The same
// geometry is in web/public/favicon.svg and docs/images/logo*.svg.
withDefaults(defineProps<{ size?: number }>(), { size: 28 })
</script>

<template>
  <span class="mark" :style="{ width: size + 'px', height: size + 'px' }" aria-hidden="true">
    <svg :width="Math.round(size * 0.62)" :height="Math.round(size * 0.62)" viewBox="0 0 24 24" fill="currentColor">
      <rect x="3" y="15.5" width="18" height="4" rx="1" />
      <rect x="6" y="10" width="12" height="4" rx="1" />
      <rect x="9" y="4.5" width="6" height="4" rx="1" />
    </svg>
  </span>
</template>

<style scoped>
.mark { flex: none; border-radius: 22%; background: var(--accent); color: var(--on-accent); display: grid; place-items: center; }
</style>
