import { ref, watch } from 'vue'

// Paper is the default; the choice is remembered per browser. It's applied
// as <html data-theme>, which every colour token keys off (styles/tokens.css).
export type Theme = 'paper' | 'dark'
const KEY = 'groundwork.theme'

function stored(): Theme {
  try {
    const v = localStorage.getItem(KEY)
    return v === 'dark' ? 'dark' : 'paper'
  } catch { return 'paper' }
}

export const theme = ref<Theme>(stored())

function apply(t: Theme) {
  document.documentElement.dataset.theme = t
}
apply(theme.value)

watch(theme, (t) => {
  apply(t)
  try { localStorage.setItem(KEY, t) } catch { /* private mode: the choice lasts this page only */ }
})

export function toggleTheme() {
  theme.value = theme.value === 'dark' ? 'paper' : 'dark'
}

/** Resolves a token (e.g. '--fail') to its current colour, for SVG that is exported. */
export function token(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}
