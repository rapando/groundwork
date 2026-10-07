import { onBeforeUnmount, ref } from 'vue'

/** A ticking clock for live durations ("running 41s"). */
export function useNow(intervalMs = 1000) {
  const now = ref(Date.now())
  const id = window.setInterval(() => (now.value = Date.now()), intervalMs)
  onBeforeUnmount(() => window.clearInterval(id))
  return now
}
