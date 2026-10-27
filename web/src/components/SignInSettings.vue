<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { serviceApi } from '../api/client'
import { describe, MIN_PASSWORD, passkeysSupported, registerPasskey, signOut, type Account } from '../api/auth'

// The sign-in methods of this service: one password and any number of
// passkeys. The service refuses to remove the last one.
const account = ref<Account | null>(null)
const error = ref('')
const busy = ref(false)
const editing = ref(false)
const current = ref('')
const next = ref('')
const name = ref('')
const done = ref('')

async function load() {
  try {
    account.value = await serviceApi<Account>('/auth')
  } catch (e) {
    error.value = describe(e)
  }
}
onMounted(load)

async function act(f: () => Promise<unknown>, ok = '') {
  if (busy.value) return
  busy.value = true
  error.value = ''
  done.value = ''
  try {
    await f()
    done.value = ok
    await load()
  } catch (e) {
    error.value = describe(e)
  } finally {
    busy.value = false
  }
}

function savePassword() {
  if (next.value.length < MIN_PASSWORD) {
    error.value = `Use at least ${MIN_PASSWORD} characters.`
    return
  }
  return act(async () => {
    await serviceApi('/auth/password', { method: 'PUT', body: { current: current.value, password: next.value } })
    editing.value = false
    current.value = next.value = ''
  }, 'Password saved.')
}

const addPasskey = () =>
  act(async () => {
    await registerPasskey(name.value)
    name.value = ''
  }, 'Passkey added.')

const fmt = (t?: string) => (t ? new Date(t).toLocaleDateString(undefined, { dateStyle: 'medium' }) : 'never')
const canRemove = () => (account.value?.passkeys.length ?? 0) + (account.value?.password ? 1 : 0) > 1

async function signOutHere() {
  await signOut().catch(() => {})
  location.reload()
}
</script>

<template>
  <div class="card settings">
    <h2 id="signin-title">Sign-in</h2>
    <p v-if="!account && !error" class="muted">Loading…</p>
    <template v-if="account">
      <section class="sec" aria-labelledby="pw-h">
        <div class="between">
          <h3 id="pw-h">Password</h3>
          <span class="mono state">{{ account.password ? 'set' : 'not set' }}</span>
        </div>
        <form v-if="editing" class="form" @submit.prevent="savePassword">
          <label v-if="account.password" class="field"><span>Current password</span>
            <input v-model="current" class="inp" type="password" autocomplete="current-password" required></label>
          <label class="field"><span>New password</span>
            <input v-model="next" class="inp" type="password" autocomplete="new-password" required></label>
          <div class="row">
            <button class="btn sm primary" type="submit" :disabled="busy || !next">Save password</button>
            <button class="btn sm" type="button" @click="editing = false">Cancel</button>
          </div>
        </form>
        <div v-else class="row">
          <button class="btn sm" type="button" @click="editing = true; done = ''">{{ account.password ? 'Change password' : 'Set a password' }}</button>
          <button v-if="account.password" class="btn sm" type="button" :disabled="busy || !canRemove()"
                  :title="canRemove() ? '' : 'Add a passkey first: this is the only way to sign in'"
                  @click="act(() => serviceApi('/auth/password', { method: 'DELETE' }), 'Password removed.')">Remove password</button>
        </div>
      </section>

      <section class="sec" aria-labelledby="pk-h">
        <h3 id="pk-h">Passkeys</h3>
        <p class="muted small">Unlock with Touch ID, Windows Hello, your phone or a security key: whatever this browser offers.</p>
        <ul v-if="account.passkeys.length" class="keys" aria-label="Passkeys">
          <li v-for="k in account.passkeys" :key="k.id" class="key">
            <div class="kmain">
              <span class="kname">{{ k.name }}</span>
              <span class="mono kmeta">added {{ fmt(k.created) }} · last used {{ fmt(k.last_used) }}</span>
            </div>
            <button class="btn sm" type="button" :disabled="busy || !canRemove()" :aria-label="`Remove ${k.name}`"
                    :title="canRemove() ? '' : 'This is the only way to sign in'"
                    @click="act(() => serviceApi(`/auth/passkeys/${encodeURIComponent(k.id)}`, { method: 'DELETE' }), 'Passkey removed.')">Remove</button>
          </li>
        </ul>
        <form v-if="account.passkeys_available && passkeysSupported()" class="row" @submit.prevent="addPasskey">
          <input v-model="name" class="inp" placeholder="Name (optional), e.g. Work laptop" aria-label="Passkey name" maxlength="64">
          <button class="btn sm" type="submit" :disabled="busy">Add a passkey</button>
        </form>
        <p v-else class="muted small">Passkeys can be added when the console is open at <span class="mono">http://localhost</span>.</p>
      </section>

      <section class="sec" aria-labelledby="so-h">
        <h3 id="so-h">Sessions</h3>
        <p class="muted small">A sign-in lasts 7 days in each browser.</p>
        <div class="row">
          <button class="btn sm" type="button" @click="signOutHere">Sign out</button>
          <button class="btn sm" type="button" :disabled="busy" @click="act(() => serviceApi('/auth/sign-out-others', { method: 'POST' }), 'Every other browser is signed out.')">Sign out other browsers</button>
        </div>
      </section>
    </template>
    <p v-if="done" class="ok small" role="status">{{ done }}</p>
    <p v-if="error" class="err" role="alert">{{ error }}</p>
    <slot />
  </div>
</template>

<style scoped>
.settings { gap: 20px; }
h2 { margin: 0; font-size: 18px; font-weight: 600; }
h3 { margin: 0; font-size: 15px; font-weight: 600; }
.sec { display: flex; flex-direction: column; gap: 10px; padding-top: 16px; border-top: 1px solid var(--line); }
.between { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.state { font-size: 13px; color: var(--text-muted); }
.form { display: flex; flex-direction: column; gap: 10px; }
.row { display: flex; flex-wrap: wrap; gap: 8px; }
.row .inp { flex: 1 1 220px; min-width: 0; height: 34px; }
.small { margin: 0; font-size: 13px; line-height: 1.5; }
.keys { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
.key { display: flex; align-items: center; gap: 12px; padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--r-control); }
.kmain { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.kname { font-weight: 500; overflow-wrap: anywhere; }
.kmeta { font-size: 13px; color: var(--text-muted); }
</style>
