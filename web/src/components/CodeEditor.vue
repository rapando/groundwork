<script setup lang="ts">
import { onBeforeUnmount, onMounted, shallowRef, watch } from 'vue'
import { EditorState, type Extension } from '@codemirror/state'
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

// Colours follow the design: keywords blue, strings lime, references amber, comments grey.
const highlight = HighlightStyle.define([
  { tag: [t.keyword, t.definitionKeyword, t.modifier], color: '#7CC0FF' },
  { tag: [t.string, t.special(t.string)], color: '#B8F36B' },
  { tag: [t.variableName, t.propertyName, t.labelName], color: '#F5B647' },
  { tag: [t.bool, t.null, t.number, t.atom], color: '#7CC0FF' },
  { tag: [t.comment, t.lineComment, t.blockComment], color: '#6E7A82', fontStyle: 'italic' },
  { tag: [t.operator, t.punctuation, t.bracket], color: '#9AA4AB' },
  { tag: [t.typeName, t.className], color: '#B79CFF' },
])

const theme = EditorView.theme({
  '&': { height: '100%', backgroundColor: '#0D1012', color: '#E6E9EB', fontSize: '13px' },
  '.cm-scroller': { fontFamily: "var(--font-mono)", lineHeight: '22px', fontVariantLigatures: 'none' },
  '.cm-content': { caretColor: '#B8F36B', padding: '14px 0' },
  '.cm-gutters': { backgroundColor: '#0D1012', color: '#5B666D', border: 'none' },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 16px 0 8px', minWidth: '44px' },
  '.cm-activeLine': { backgroundColor: 'rgba(255,255,255,.03)' },
  '.cm-activeLineGutter': { backgroundColor: 'rgba(255,255,255,.03)', color: '#9AA4AB' },
  '&.cm-focused': { outline: 'none' },
  '&.cm-focused .cm-cursor': { borderLeftColor: '#B8F36B' },
  '.cm-selectionBackground, &.cm-focused .cm-selectionBackground': { backgroundColor: 'rgba(124,192,255,.25) !important' },
  '.cm-tooltip': { backgroundColor: '#12161A', border: '1px solid #2A3238', color: '#E6E9EB', borderRadius: '6px' },
  '.cm-tooltip-lint .cm-diagnostic': { padding: '8px 10px', borderLeft: '3px solid #FF7A7A', whiteSpace: 'pre-wrap' },
  '.cm-diagnostic-warning': { borderLeftColor: '#F5B647 !important' },
  '.cm-diagnostic-info': { borderLeftColor: '#7CC0FF !important' },
  '.cm-diagnosticAction': { backgroundColor: '#B8F36B', color: '#0D1012', borderRadius: '4px', padding: '2px 8px', marginLeft: '0', marginTop: '6px' },
  '.cm-diagnostic-error': { borderLeftColor: '#FF7A7A' },
  '.cm-lintRange-error': { backgroundImage: 'none', textDecoration: 'underline wavy #FF7A7A', textUnderlineOffset: '4px' },
  '.cm-lintRange-warning': { backgroundImage: 'none', textDecoration: 'underline wavy #F5B647', textUnderlineOffset: '4px' },
  '.cm-lintRange-info': { backgroundImage: 'none', textDecoration: 'underline wavy #7CC0FF', textUnderlineOffset: '4px' },
  '.cm-foldGutter span': { color: '#5B666D' },
}, { dark: true })

function makeState(doc: string, path: string): EditorState {
  return EditorState.create({
    doc,
    extensions: [
      lineNumbers(), foldGutter(), lintGutter(), highlightActiveLine(), highlightActiveLineGutter(),
      drawSelection(), history(), indentOnInput(), bracketMatching(), highlightSelectionMatches(),
      syntaxHighlighting(highlight), theme, language(path),
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
