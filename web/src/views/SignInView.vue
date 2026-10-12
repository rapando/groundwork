<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import BrandMark from '../components/BrandMark.vue'
import ThemeToggle from '../components/ThemeToggle.vue'
import {
  AuthError, authStatus, describe, loginPassword, loginPasskey, passkeysSupported, registerPasskey, setupPassword,
  MIN_PASSWORD, type AuthStatus,
} from '../api/auth'

// Shown instead of the console until this browser signs in. On first run
// (nothing set up yet) it sets sign-in up instead.
const status = ref<AuthStatus | null>(null)
const loadError = ref('')
const password = ref('')
const confirm = ref('')
const busy = ref(false)
const error = ref('')

const canPasskey = computed(() => !!status.value?.passkeys_available && passkeysSupported())
const mode = computed(() => {
  const s = status.value
  if (!s) return 'loading'
  if (!s.configured) return s.setup_allowed ? 'setup' : 'remote-setup'
  return 'signin'
})
const port = window.location.port
const onLocalhost = `http://localhost:${port}${window.location.pathname}`
const mismatch = computed(() => mode.value === 'setup' && confirm.value !== '' && confirm.value !== password.value)

onMounted(async () => {
  document.title = 'Sign in · groundwork'
  try {
    status.value = await authStatus()
    // signed in, the service serves the console; a page without it came from elsewhere (the Vite dev server)
    if (status.value.signed_in) loadError.value = 'Signed in, but this page has no API token. Open the console from the groundwork service.'
    if (mode.value === 'setup') document.title = 'Set up sign-in · groundwork'
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  }
})

async function run(f: () => Promise<unknown>) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await f()
    location.reload() // the page now comes back with the console
  } catch (e) {
    error.value = e instanceof AuthError && e.code === 'configured' ? 'Sign-in was just set up elsewhere. Reload to sign in.' : describe(e)
    busy.value = false
  }
}

function submitPassword() {
  if (mode.value === 'setup') {
    if (password.value.length < MIN_PASSWORD) {
      error.value = `Use at least ${MIN_PASSWORD} characters.`
      return
    }
    if (password.value !== confirm.value) {
      error.value = "The passwords don't match."
      return
    }
    return run(() => setupPassword(password.value))
  }
  return run(() => loginPassword(password.value))
}
</script>

<template>
  <div class="shell">
    <header class="top">
      <BrandMark />
      <span class="mono name">groundwork</span>
      <ThemeToggle class="theme" />
    </header>

    <main class="body">
      <p v-if="loadError" class="err" role="alert">{{ loadError }}</p>
      <p v-else-if="mode === 'loading'" class="muted" aria-live="polite">Loading…</p>

      <section v-else-if="mode === 'remote-setup'" class="card panel" aria-labelledby="t">
        <h1 id="t">Sign-in isn't set up yet</h1>
        <p class="muted">
          For safety, sign-in can only be set up on the machine groundwork runs on. Open
          <span class="mono">http://localhost:{{ port }}</span> there first.
        </p>
      </section>

      <section v-else class="card panel" aria-labelledby="t">
        <div class="head">
          <h1 id="t">{{ mode === 'setup' ? 'Set up sign-in' : 'Sign in' }}</h1>
          <p v-if="mode === 'setup'" class="muted">
            groundwork runs Terraform and Ansible with your credentials. Choose how you'll unlock its console:
            the same fingerprint or device PIN you use for this machine, a password, or both. You can change this later from the project list.
          </p>
        </div>

        <template v-if="canPasskey && (mode === 'setup' || status?.passkeys)">
          <button class="btn primary big" type="button" :disabled="busy" @click="run(mode === 'setup' ? () => registerPasskey() : loginPasskey)">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><path d="M12 11v4M8.5 8.5a5 5 0 0 1 8.5 3.5c0 3-1 5.5-2.5 7.5M6 12a6 6 0 0 1 .8-3M7 15.5c.5 1.5.6 2.8.5 4M10 21c.8-1.8 1.4-4 1.5-6.5M4.5 9A8 8 0 0 1 19 7.5M20 12c0 1.5-.2 3-.6 4.3" /></svg>
            {{ mode === 'setup' ? 'Use Touch ID or a passkey' : 'Sign in with a passkey' }}
          </button>
          <div v-if="mode === 'setup' || status?.password" class="or"><span>or</span></div>
        </template>
        <p v-else-if="mode === 'signin' && status?.passkeys && !status.password" class="muted">
          This console signs in with a passkey, which only works at
          <a :href="onLocalhost">http://localhost:{{ port }}</a>.
        </p>

        <form v-if="mode === 'setup' || status?.password" class="form" @submit.prevent="submitPassword">
          <label class="field">
            <span>{{ mode === 'setup' ? 'Choose a password' : 'Password' }}</span>
            <input v-model="password" class="inp" type="password" :disabled="busy" :autocomplete="mode === 'setup' ? 'new-password' : 'current-password'" :autofocus="!canPasskey" required>
          </label>
          <label v-if="mode === 'setup'" class="field">
            <span>Confirm it</span>
            <input v-model="confirm" class="inp" :class="{ bad: mismatch }" type="password" :disabled="busy" autocomplete="new-password" required>
          </label>
          <button class="btn" :class="{ primary: !canPasskey }" type="submit" :disabled="busy || !password">
            {{ busy ? 'Working…' : mode === 'setup' ? 'Set password' : 'Sign in' }}
          </button>
        </form>

        <p v-if="error" class="err" role="alert">{{ error }}</p>
        <p v-if="mode === 'signin'" class="hint">Locked out? Run <span class="mono">groundwork auth reset</span> on this machine to set sign-in up again.</p>
      </section>
    </main>
  </div>
</template>

<style scoped>
.shell { min-height: 100vh; }
.top { display: flex; align-items: center; gap: 12px; padding: 16px 32px; border-bottom: 1px solid var(--line); }
.name { font-weight: 700; }
.theme { margin-left: auto; }
.body { max-width: 460px; margin: 0 auto; padding: 12vh 16px 40px; }
.panel { gap: 18px; padding: 28px; }
.head { display: flex; flex-direction: column; gap: 8px; }
h1 { margin: 0; font-size: 24px; font-weight: 600; letter-spacing: -.01em; }
.panel p { margin: 0; font-size: 15px; line-height: 1.6; }
.big { height: 46px; font-size: 15px; }
.or { display: flex; align-items: center; gap: 12px; color: var(--text-faint); font-size: 13px; }
.or::before, .or::after { content: ''; flex: 1; border-top: 1px solid var(--line); }
.form { display: flex; flex-direction: column; gap: 14px; }
.hint { font-size: 13px !important; color: var(--text-muted); }
.hint .mono { color: var(--text-code); }
@media (max-width: 640px) { .top { padding: 14px 16px; } .body { padding-top: 6vh; } }
</style>
