<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import InlineConfirm from './InlineConfirm.vue'
import { api } from '../api/client'
import type { Issue, IssueStep, RunBrief } from '../api/types'
import { cleanError, heldFor, lockOperation, parseGoTime, parseLockInfo } from '../stores/runs'

/**
 * Why a failed run stopped: the Troubleshoot issue it matched (if any) with
 * the facts that tell a stale lock from a live job, what already changed, and
 * the next step. Without a match it is the error itself, cleaned up.
 */
const props = defineProps<{
  run: RunBrief
  error: string
  issue: Issue | null
  /** where it stopped: the failed resource or hosts */
  stoppedAt: string
  /** for terraform applies: resources done out of planned */
  applied: { done: number; total: number } | null
  /** the run stopped before applying anything */
  beforeApply: boolean
  now: number
}>()
const emit = defineEmits<{ (e: 'log'): void }>()
const router = useRouter()

const headline = computed(() => cleanError(props.error))
const raw = ref(false)
// the run's error line, then the log lines the cause was matched on (the lock ID and holder live there)
const rawText = computed(() => [props.error, props.issue?.excerpt].filter(Boolean).join('\n\n'))

// ---- facts ----
const lock = computed(() => {
  const l = parseLockInfo(props.error)
  const f = props.issue?.facts ?? {}
  const who = l?.who ?? f.who
  const created = l?.created ?? (f.created ? parseGoTime(f.created) : undefined)
  const op = lockOperation(l?.operation ?? f.operation)
  return who || created ? { who, created, op } : null
})
const hm = (t: number) => new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })
const facts = computed(() => {
  const out: [string, string][] = []
  const l = lock.value
  if (l?.who) out.push(['Held by', l.who])
  if (l?.created) out.push(['Since', `${hm(l.created)} (${heldFor(l.created, props.now)})`])
  if (l?.op) out.push(['Operation', l.op])
  if (props.stoppedAt) out.push(['Stopped at', props.stoppedAt])
  if (props.applied) out.push(['Changes', `applied ${props.applied.done} of ${props.applied.total} · nothing was rolled back`])
  else if (props.beforeApply) out.push(['Changes', 'none: it stopped before applying anything'])
  return out
})
const guidance = computed(() => {
  if (!props.issue) return ''
  if (lock.value) {
    const who = lock.value.who ? `the job on ${lock.value.who}` : 'the job holding it'
    return `If ${who} is still running, wait for it and plan again. Release the lock only if that job is gone.`
  }
  return props.issue.explain
})

// ---- actions ----
const isTf = computed(() => props.run.kind.startsWith('tf.'))
const againLabel = computed(() => (isTf.value ? 'Plan again' : props.run.kind === 'ans.playbook' || props.run.kind === 'ans.check' ? 'Dry run again' : ''))
const busy = ref(false)
const actError = ref('')
async function again() {
  busy.value = true
  actError.value = ''
  try {
    const r = await api<{ run: { id: number } }>(`/runs/${props.run.id}/replan`, { method: 'POST', body: {} })
    router.push('/runs/' + r.run.id)
  } catch (e) { actError.value = e instanceof Error ? e.message : String(e) } finally { busy.value = false }
}

// the issue's first step with an action, e.g. "Release the lock"
const first = computed<{ step: IssueStep; i: number } | null>(() => {
  if (props.issue?.status !== 'open') return null
  const i = props.issue.steps.findIndex((s) => s.action)
  return i < 0 ? null : { step: props.issue.steps[i], i }
})
const asking = ref(false)
const typed = ref('')
const checks = ref<Issue['checks']>([])
const checksOk = computed(() => (checks.value ?? []).every((c) => c.ok))
watch(() => props.issue?.id, () => { asking.value = false; typed.value = ''; actError.value = '' })
async function ask() {
  asking.value = true
  typed.value = ''
  actError.value = ''
  checks.value = []
  // fresh pre-checks: is anything still running against this state?
  try { checks.value = (await api<Issue>(`/issues/${props.issue!.id}`)).checks ?? [] } catch { /* the server checks again on act */ }
}
async function act() {
  const f = first.value
  if (!f || !props.issue) return
  busy.value = true
  actError.value = ''
  try {
    const r = await api<{ run: { id: number } }>(`/issues/${props.issue.id}/act`, { body: { step: f.i, confirm: typed.value } })
    asking.value = false
    router.push('/runs/' + r.run.id)
  } catch (e) { actError.value = e instanceof Error ? e.message : String(e) } finally { busy.value = false }
}
const copied = ref(false)
async function copy(text: string) {
  try { await navigator.clipboard.writeText(text); copied.value = true; setTimeout(() => (copied.value = false), 2000) } catch { /* clipboard blocked */ }
}
</script>

<template>
  <section class="fc" :class="{ known: issue }" aria-labelledby="fc-title">
    <div class="top">
      <h2 id="fc-title" class="title">{{ issue ? issue.title : headline || 'The run failed' }}</h2>
      <span v-if="issue" class="pill">known cause</span>
    </div>
    <p v-if="issue && headline" class="mono small muted line">{{ headline }}</p>
    <div v-if="facts.length" class="kv">
      <template v-for="[k, v] in facts" :key="k"><span class="k">{{ k }}</span><span class="mono v">{{ v }}</span></template>
    </div>
    <p v-if="guidance" class="guide">{{ guidance }}</p>
    <p v-if="issue?.status === 'resolved'" class="small ok">Fixed: {{ issue.resolution }}</p>

    <div class="row">
      <button v-if="issue && againLabel" class="btn sm primary" type="button" :disabled="busy" @click="again">{{ againLabel }}</button>
      <template v-if="first && !asking">
        <button v-if="first.step.action!.kind === 'run'" class="btn sm" :class="{ danger: first.step.action!.mutating }" type="button" :aria-expanded="asking" @click="ask">{{ first.step.title }}…</button>
        <RouterLink v-else-if="first.step.action!.kind === 'open' && first.step.action!.href" class="btn sm" :to="first.step.action!.href">{{ first.step.action!.label }}</RouterLink>
        <button v-else-if="first.step.action!.kind === 'copy'" class="btn sm" type="button" @click="copy(first.step.action!.text || first.step.action!.command || '')">{{ copied ? 'Copied' : first.step.action!.label }}</button>
      </template>
      <button class="btn sm" type="button" @click="emit('log')">Show in log</button>
      <button class="btn sm" type="button" :aria-expanded="raw" aria-controls="fc-raw" @click="raw = !raw">Raw error</button>
      <RouterLink v-if="issue" class="small link" :to="'/troubleshoot/' + issue.id">Open in Troubleshoot →</RouterLink>
    </div>

    <InlineConfirm v-if="first && asking" :message="first.step.title + '?'" :confirm-label="first.step.action!.label" cancel-label="Not now"
      :danger="first.step.action!.mutating" :busy="busy"
      :disabled="(!!first.step.action!.confirm && typed !== first.step.action!.confirm) || (!!first.step.action!.mutating && !checksOk)"
      @confirm="act" @cancel="asking = false">
      <p v-if="first.step.detail" class="small muted m0">{{ first.step.detail }}</p>
      <ul v-if="checks?.length" class="checks small">
        <li v-for="(c, i) in checks" :key="i" :class="c.ok ? 'ok' : 'fail'"><span aria-hidden="true">{{ c.ok ? '✓' : '!' }}</span> {{ c.detail }}</li>
      </ul>
      <code v-if="first.step.action!.command" class="mono cmd">{{ first.step.action!.command }}</code>
      <label v-if="first.step.action!.confirm" class="field small">
        <span>Type <b class="mono">{{ first.step.action!.confirm }}</b> to confirm</span>
        <input v-model="typed" class="inp" type="text" autocomplete="off" :aria-label="`Type ${first.step.action!.confirm} to confirm`" @keydown.enter="act" />
      </label>
    </InlineConfirm>
    <p v-if="actError" class="err" role="alert">{{ actError }}</p>
    <pre v-if="raw" id="fc-raw" class="raw mono">{{ rawText }}</pre>
  </section>
</template>

<style scoped>
.fc { display: flex; flex-direction: column; gap: 10px; padding: 14px 16px; border-radius: 8px; border: 1px solid var(--fail-line); background: var(--fail-bg); }
.top { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.title { margin: 0; font-size: 15px; font-weight: 600; color: var(--fail-strong); overflow-wrap: anywhere; }
.pill { font: 500 11.5px var(--font-mono); padding: 2px 7px; border-radius: 4px; text-transform: uppercase; letter-spacing: .04em; color: var(--fail-strong); background: color-mix(in srgb, var(--fail) 12%, transparent); }
.small { font-size: 13px; }
.line { margin: 0; overflow-wrap: anywhere; }
.kv { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 4px 14px; font-size: 13.5px; }
.k { color: var(--text-muted); }
.v { color: var(--text); overflow-wrap: anywhere; }
.guide { margin: 0; font-size: 14px; color: var(--text-soft); max-width: 760px; }
.row { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.btn.danger { border-color: var(--fail-line); color: var(--fail-strong); }
.link { margin-left: 4px; }
.m0 { margin: 0; }
.checks { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }
.cmd { font-size: 13px; background: var(--bg-inset); border: 1px solid var(--line); border-radius: 6px; padding: 8px 10px; overflow-x: auto; white-space: nowrap; color: var(--text-code); }
.field .inp { width: 240px; max-width: 100%; height: 34px; }
.raw { margin: 0; padding: 10px 12px; font-size: 13px; line-height: 1.6; background: var(--bg-inset); border: 1px solid var(--line); border-radius: 6px; color: var(--fail-strong); white-space: pre-wrap; overflow-wrap: anywhere; max-height: 320px; overflow: auto; }
</style>
