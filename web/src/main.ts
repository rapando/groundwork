import '@fontsource/ibm-plex-sans/latin-400.css'
import '@fontsource/ibm-plex-sans/latin-500.css'
import '@fontsource/ibm-plex-sans/latin-600.css'
import '@fontsource/jetbrains-mono/latin-400.css'
import '@fontsource/jetbrains-mono/latin-500.css'
import '@fontsource/jetbrains-mono/latin-700.css'
import './styles/base.css'
import './composables/theme'
import { createApp } from 'vue'
import { createPinia } from 'pinia'

// The server puts the API token in the page only for a signed-in browser;
// without it the page is the sign-in (or first-run setup) screen.
if (document.querySelector('meta[name="groundwork-token"]')) {
  void Promise.all([import('./App.vue'), import('./router')]).then(([{ default: App }, { router }]) =>
    createApp(App).use(createPinia()).use(router).mount('#app'),
  )
} else {
  void import('./views/SignInView.vue').then(({ default: SignInView }) => createApp(SignInView).mount('#app'))
}
