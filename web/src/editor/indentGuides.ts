import { countColumn, RangeSetBuilder, type EditorState, type Extension } from '@codemirror/state'
import { Decoration, EditorView, ViewPlugin, type DecorationSet, type ViewUpdate } from '@codemirror/view'
import { getIndentUnit } from '@codemirror/language'

/** Indent depth of a line in columns, or -1 for a blank line. */
function indentOf(state: EditorState, text: string): number {
  const m = /^[ \t]*/.exec(text)![0]
  return m.length === text.length ? -1 : countColumn(m, state.tabSize)
}

/**
 * Guide levels for each line in [fromLine, toLine]. A blank line inside a block
 * takes the shallower of its neighbours' depths so the guides run through it.
 */
export function guideLevels(state: EditorState, fromLine: number, toLine: number): number[] {
  const unit = getIndentUnit(state) || 2
  const doc = state.doc
  const nearest = (n: number, step: 1 | -1): number => {
    // look a bounded distance so a huge run of blank lines stays cheap
    for (let i = n, k = 0; i >= 1 && i <= doc.lines && k < 200; i += step, k++) {
      const d = indentOf(state, doc.line(i).text)
      if (d >= 0) return d
    }
    return 0
  }
  const out: number[] = []
  for (let n = fromLine; n <= toLine; n++) {
    let d = indentOf(state, doc.line(n).text)
    if (d < 0) d = Math.min(nearest(n - 1, -1), nearest(n + 1, 1))
    out.push(Math.floor(d / unit))
  }
  return out
}

const marks = new Map<string, Decoration>()
function mark(levels: number, unit: number): Decoration {
  const key = levels + ':' + unit
  let d = marks.get(key)
  if (!d) {
    d = Decoration.line({
      class: 'cm-indent-guides',
      attributes: { style: `--gw-guide-unit: ${unit}ch; --gw-guide-width: ${levels * unit}ch` },
    })
    marks.set(key, d)
  }
  return d
}

function build(view: EditorView): DecorationSet {
  const b = new RangeSetBuilder<Decoration>()
  const unit = getIndentUnit(view.state) || 2
  const doc = view.state.doc
  for (const { from, to } of view.visibleRanges) {
    const first = doc.lineAt(from).number
    const last = doc.lineAt(to).number
    const levels = guideLevels(view.state, first, last)
    levels.forEach((lv, i) => {
      if (lv > 0) b.add(doc.line(first + i).from, doc.line(first + i).from, mark(lv, unit))
    })
  }
  return b.finish()
}

const plugin = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet
    constructor(view: EditorView) {
      this.decorations = build(view)
    }
    update(u: ViewUpdate) {
      if (u.docChanged || u.viewportChanged || u.geometryChanged) this.decorations = build(u.view)
    }
  },
  { decorations: (v) => v.decorations },
)

/**
 * Faint vertical lines at each indentation level, drawn as a background on the
 * line so they cost no DOM nodes. One guide per level, up to the line's indent.
 */
export function indentGuides(): Extension {
  return [
    plugin,
    EditorView.baseTheme({
      '.cm-line.cm-indent-guides': {
        backgroundImage: 'repeating-linear-gradient(to right, var(--gw-guide-color) 0 1px, transparent 1px var(--gw-guide-unit))',
        backgroundSize: 'var(--gw-guide-width) 100%',
        backgroundRepeat: 'no-repeat',
        backgroundOrigin: 'content-box',
      },
    }),
  ]
}
