<script setup lang="ts">
import { onBeforeUnmount, onMounted, shallowRef, watch } from 'vue'
import { Compartment, EditorState, type Extension } from '@codemirror/state'
import {
  EditorView, drawSelection, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers,
} from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import {
  HighlightStyle, StreamLanguage, bracketMatching, foldGutter, indentOnInput, syntaxHighlighting,
} from '@codemirror/language'
import { lintGutter, setDiagnostics, type Diagnostic as CMDiagnostic } from '@codemirror/lint'
import { highlightSelectionMatches, searchKeymap } from '@codemirror/search'
import { yaml } from '@codemirror/lang-yaml'
import { json } from '@codemirror/lang-json'
import { properties } from '@codemirror/legacy-modes/mode/properties'
import { tags as t } from '@lezer/highlight'
import { hcl } from 'codemirror-lang-hcl'
import type { Diagnostic, Edit } from '../api/types'
import { theme } from '../composables/theme'

const props = defineProps<{
  path: string
  doc: string
  /** bump to replace the buffer with `doc` (load / reload); typing never changes it */
  version: number
  diagnostics: Diagnostic[]
}>()
const emit = defineEmits<{
  (e: 'change', text: string): void
  (e: 'save'): void
  (e: 'cursor', pos: { line: number; col: number }): void
  (e: 'fix', d: Diagnostic): void
}>()

const host = shallowRef<HTMLDivElement>()
const view = shallowRef<EditorView>()
let programmatic = false

function language(path: string): Extension[] {
  if (/\.(tf|tfvars|hcl)$/.test(path)) return [hcl()]
  if (/\.ya?ml$/.test(path)) return [yaml()]
  if (path.endsWith('.json')) return [json()]
  if (/\.(cfg|ini)$/.test(path) || path.endsWith('ansible.cfg')) return [StreamLanguage.define(properties)]
  return []
}

// Colours come from the theme tokens (--syn-*): keywords blue, strings green,
// references amber, comments grey, in a light and a dark palette.
const highlight = HighlightStyle.define([
  { tag: [t.keyword, t.definitionKeyword, t.modifier], color: 'var(--syn-keyword)' },
  { tag: [t.string, t.special(t.string)], color: 'var(--syn-string)' },
  { tag: [t.variableName, t.propertyName, t.labelName], color: 'var(--syn-property)' },
  { tag: [t.bool, t.null, t.number, t.atom], color: 'var(--syn-number)' },
  { tag: [t.comment, t.lineComment, t.blockComment], color: 'var(--syn-comment)', fontStyle: 'italic' },
  { tag: [t.operator, t.punctuation, t.bracket], color: 'var(--syn-operator)' },
  { tag: [t.typeName, t.className], color: 'var(--syn-type)' },
])

const styles = {
  '&': { height: '100%', backgroundColor: 'var(--bg-card)', color: 'var(--text)', fontSize: '14px' },
  '.cm-scroller': { fontFamily: "var(--font-mono)", lineHeight: '23px', fontVariantLigatures: 'none' },
  '.cm-content': { caretColor: 'var(--text)', padding: '14px 0' },
  '.cm-gutters': { backgroundColor: 'var(--bg-card)', color: 'var(--syn-gutter)', border: 'none' },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 16px 0 8px', minWidth: '44px' },
  '.cm-activeLine': { backgroundColor: 'var(--syn-active-line)' },
  '.cm-activeLineGutter': { backgroundColor: 'var(--syn-active-line)', color: 'var(--text-soft)' },
  '&.cm-focused': { outline: 'none' },
  '&.cm-focused .cm-cursor': { borderLeftColor: 'var(--text)' },
  '.cm-selectionBackground, &.cm-focused .cm-selectionBackground': { backgroundColor: 'var(--syn-selection) !important' },
  '.cm-selectionMatch': { backgroundColor: 'var(--syn-selection)' },
  '.cm-matchingBracket, &.cm-focused .cm-matchingBracket': { backgroundColor: 'var(--syn-selection)', outline: 'none' },
  '.cm-tooltip': { backgroundColor: 'var(--bg-panel)', border: '1px solid var(--line-strong)', color: 'var(--text)', borderRadius: '6px', boxShadow: 'var(--shadow)' },
  '.cm-tooltip-lint .cm-diagnostic': { padding: '8px 10px', borderLeft: '3px solid var(--fail)', whiteSpace: 'pre-wrap' },
  '.cm-diagnostic-warning': { borderLeftColor: 'var(--warn) !important' },
  '.cm-diagnostic-info': { borderLeftColor: 'var(--running) !important' },
  '.cm-diagnosticAction': { backgroundColor: 'var(--accent)', color: 'var(--on-accent)', borderRadius: '4px', padding: '2px 8px', marginLeft: '0', marginTop: '6px' },
  '.cm-diagnostic-error': { borderLeftColor: 'var(--fail)' },
  '.cm-lintRange-error': { backgroundImage: 'none', textDecoration: 'underline wavy var(--fail)', textUnderlineOffset: '4px' },
  '.cm-lintRange-warning': { backgroundImage: 'none', textDecoration: 'underline wavy var(--warn)', textUnderlineOffset: '4px' },
  '.cm-lintRange-info': { backgroundImage: 'none', textDecoration: 'underline wavy var(--running)', textUnderlineOffset: '4px' },
  '.cm-foldGutter span': { color: 'var(--syn-gutter)' },
  '.cm-panels': { backgroundColor: 'var(--bg-panel)', color: 'var(--text)' },
  '.cm-searchMatch': { backgroundColor: 'color-mix(in srgb, var(--warn) 25%, transparent)' },
}
// The colours above follow the tokens on their own; the compartment switches
// CodeMirror's own light/dark defaults (panels, search, scrollbars) to match.
const themes = { paper: EditorView.theme(styles, { dark: false }), dark: EditorView.theme(styles, { dark: true }) }
const themeSlot = new Compartment()

function makeState(doc: string, path: string): EditorState {
  return EditorState.create({
    doc,
    extensions: [
      lineNumbers(), foldGutter(), lintGutter(), highlightActiveLine(), highlightActiveLineGutter(),
      drawSelection(), history(), indentOnInput(), bracketMatching(), highlightSelectionMatches(),
      syntaxHighlighting(highlight), themeSlot.of(themes[theme.value]), language(path),
      keymap.of([
        { key: 'Mod-s', preventDefault: true, run: () => { emit('save'); return true } },
        indentWithTab, ...searchKeymap, ...historyKeymap, ...defaultKeymap,
      ]),
      EditorView.updateListener.of((u) => {
        if (u.docChanged && !programmatic) emit('change', u.state.doc.toString())
        if (u.selectionSet || u.docChanged) {
          const head = u.state.selection.main.head
          const line = u.state.doc.lineAt(head)
          emit('cursor', { line: line.number, col: head - line.from + 1 })
        }
      }),
    ],
  })
}

function toCM(v: EditorView, ds: Diagnostic[]): CMDiagnostic[] {
  const doc = v.state.doc
  const out: CMDiagnostic[] = []
  for (const d of ds) {
    if (!d.line || d.line > doc.lines) continue
    const l = doc.line(d.line)
    const from = Math.min(l.from + Math.max((d.col ?? 1) - 1, 0), l.to)
    let to = from
    if (d.end_line && d.end_line <= doc.lines) {
      const el = doc.line(d.end_line)
      to = Math.min(el.from + Math.max((d.end_col ?? 1) - 1, 0), el.to)
    }
    if (to <= from) to = Math.min(from + 1, doc.length) // zero-width findings still get a mark
    if (to <= from) to = from
    out.push({
      from, to,
      severity: d.severity,
      message: d.message + (d.detail ? '\n' + d.detail : ''),
      source: d.tool + (d.code ? ' · ' + d.code : ''),
      actions: d.fix ? [{ name: d.fix.title, apply: () => emit('fix', d) }] : undefined,
    })
  }
  return out
}

function paintDiagnostics() {
  const v = view.value
  if (v) v.dispatch(setDiagnostics(v.state, toCM(v, props.diagnostics)))
}

function setDoc() {
  const v = view.value
  if (!v) return
  programmatic = true
  v.setState(makeState(props.doc, props.path))
  programmatic = false
  paintDiagnostics()
}

onMounted(() => {
  view.value = new EditorView({ parent: host.value!, state: makeState(props.doc, props.path) })
  paintDiagnostics()
})
onBeforeUnmount(() => view.value?.destroy())

watch(() => props.version, setDoc)
watch(() => props.diagnostics, paintDiagnostics, { deep: true })
watch(theme, (th) => view.value?.dispatch({ effects: themeSlot.reconfigure(themes[th]) }))

/** Apply a quick-fix edit if the text still matches what the fix expects. */
function applyEdit(e: Edit): boolean {
  const v = view.value
  if (!v) return false
  const doc = v.state.doc
  if (e.line > doc.lines || e.end_line > doc.lines) return false
  const from = doc.line(e.line).from + e.col - 1
  const to = doc.line(e.end_line).from + e.end_col - 1
  if (doc.sliceString(from, to) !== e.old) return false
  v.dispatch({ changes: { from, to, insert: e.new }, selection: { anchor: from + e.new.length }, scrollIntoView: true })
  v.focus()
  return true
}

function goTo(line: number, col = 1) {
  const v = view.value
  if (!v || line < 1 || line > v.state.doc.lines) return
  const l = v.state.doc.line(line)
  const pos = Math.min(l.from + Math.max(col - 1, 0), l.to)
  v.dispatch({ selection: { anchor: pos }, scrollIntoView: true, effects: EditorView.scrollIntoView(pos, { y: 'center' }) })
  v.focus()
}

defineExpose({ applyEdit, goTo, focus: () => view.value?.focus() })
</script>

<template>
  <div ref="host" class="editor" />
</template>

<style scoped>
.editor { height: 100%; min-height: 0; }
.editor :deep(.cm-editor) { height: 100%; }
</style>
