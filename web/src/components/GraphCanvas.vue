<script setup lang="ts">
import { computed, onMounted, ref, shallowRef, watch } from 'vue'
import { accent, theme, token } from '../composables/theme'

// rows: extra lines under the label. The system view draws each service as a
// card (status dot, name, port chips, a source line under it), with plain
// rows for the rest; star marks a node drawn more than once.
export interface GChip { text: string; tone?: 'ok' | 'warn' | 'muted' }
export interface GCard { sub?: string; subTone?: 'warn'; dot?: 'ok' | 'warn' | 'idle'; dashed?: boolean }
export interface GNodeRow { text: string; tone?: 'warn' | 'muted'; chips?: GChip[]; card?: GCard }
// meta: a note at the right of the label line; tag: a word above the box
// (how it relates to the selection), note says the same to a screen reader; fade: opacity, to push it into the background
export interface GNode {
  id: string; title: string; label: string; count?: string; state?: string; errors?: number; dim?: boolean; star?: boolean
  rows?: GNodeRow[]; meta?: string; tag?: string; note?: string; fade?: number
}
export interface GEdge { from: string; to: string; label?: string }
// group: a dashed frame with its label above it (an inventory group)
export interface GContainer { id: string; label: string; children: string[]; group?: boolean }

const props = withDefaults(defineProps<{
  nodes: GNode[]; edges: GEdge[]; containers?: GContainer[]
  selected?: string; focus?: string; direction?: 'RIGHT' | 'DOWN'; ariaLabel?: string
  // packed: boxes are packed into rows (no edges to route), groups included
  packed?: boolean; nodeWidth?: number; highlight?: string[]
  // reach: nodes and edges (from>to) to draw in the accent colour; the other edges fade
  reach?: string[]; reachEdges?: string[]
  // controls: zoom in / out / fit buttons in the top-right corner
  controls?: boolean
}>(), {
  containers: () => [], selected: '', focus: '', direction: 'RIGHT', ariaLabel: 'Graph', packed: false, nodeWidth: 210,
  highlight: () => [], reach: () => [], reachEdges: () => [], controls: false,
})
const emit = defineEmits<{ (e: 'select', id: string): void }>()

const NH = 60, ROW = 17, CHIP_ROW = 22, CARD = 38, CARD1 = 24, CH = 6.6 // monospace 11px advance
const rowH = (r: GNodeRow) => (r.card ? (r.card.sub ? CARD : CARD1) : r.chips?.length ? CHIP_ROW : ROW)
interface RowAt { r: GNodeRow; y: number; h: number } // y: offset from the top of the box
function rowsOf(n: GNode): RowAt[] {
  let y = NH + 4
  return (n.rows ?? []).map((r) => {
    const at = { r, y, h: rowH(r) }
    y += at.h
    return at
  })
}
const height = (n: GNode) => (n.rows?.length ? NH + 10 + n.rows.reduce((s, r) => s + rowH(r), 0) : NH)
interface Box { id: string; x: number; y: number; w: number; h: number; node?: GNode; container?: GContainer }
interface Path { d: string; label?: string; lx?: number; ly?: number; key: string; from: string; to: string }
const boxes = shallowRef<Box[]>([])
const paths = shallowRef<Path[]>([])
const size = ref({ w: 0, h: 0 })
const layoutError = ref('')

// ELK is large: load it only when a graph is shown.
let elk: { layout: (g: unknown) => Promise<any> } | null = null
async function getElk() {
  if (!elk) {
    const mod = await import('elkjs/lib/elk.bundled.js')
    elk = new mod.default()
  }
  return elk
}

let seq = 0
async function layout() {
  const mine = ++seq
  const containerOf = new Map<string, string>()
  for (const c of props.containers) for (const ch of c.children) containerOf.set(ch, c.id)
  const containerIds = new Set(props.containers.map((c) => c.id))
  const visible = new Set(props.nodes.map((n) => n.id))
  const leaf = (n: GNode) => ({ id: n.id, width: props.nodeWidth, height: height(n) })
  const children: any[] = []
  const pack = props.packed ? { 'elk.algorithm': 'rectpacking', 'elk.spacing.nodeNode': '16' } : {}
  for (const c of props.containers) {
    children.push({
      id: c.id,
      layoutOptions: { ...pack, 'elk.padding': c.group ? '[top=46,left=16,bottom=16,right=16]' : '[top=44,left=20,bottom=20,right=20]' },
      children: props.nodes.filter((n) => containerOf.get(n.id) === c.id).map(leaf),
    })
  }
  for (const n of props.nodes) if (!containerOf.has(n.id) && !containerIds.has(n.id)) children.push(leaf(n))
  // containment is drawn, so container→child edges are implied
  const drawn = props.edges
    .filter((e) => visible.has(e.from) && visible.has(e.to) && e.from !== e.to)
    .filter((e) => !(containerIds.has(e.from) && containerOf.get(e.to) === e.from))
  const edges = drawn.map((e, i) => ({
      id: 'e' + i, sources: [e.from], targets: [e.to],
      labels: e.label ? [{ text: e.label, width: e.label.length * 6.6 + 8, height: 14 }] : [],
    }))
  try {
    const res = await (await getElk()).layout({
      id: 'root',
      layoutOptions: props.packed
        ? {
          'elk.algorithm': 'rectpacking', 'elk.aspectRatio': '1.8', 'elk.spacing.nodeNode': '28',
          'elk.json.shapeCoords': 'ROOT', 'elk.padding': '[top=24,left=24,bottom=24,right=24]',
        }
        : {
          'elk.algorithm': 'layered', 'elk.direction': props.direction,
          'elk.hierarchyHandling': 'INCLUDE_CHILDREN',
          'elk.layered.spacing.nodeNodeBetweenLayers': '70', 'elk.spacing.nodeNode': '28',
          'elk.edgeRouting': 'ORTHOGONAL', 'elk.json.edgeCoords': 'ROOT', 'elk.json.shapeCoords': 'ROOT',
          'elk.padding': '[top=24,left=24,bottom=24,right=24]',
        },
      children, edges,
    })
    if (mine !== seq) return
    const byId = new Map(props.nodes.map((n) => [n.id, n]))
    const cById = new Map(props.containers.map((c) => [c.id, c]))
    const out: Box[] = []
    const walk = (list: any[]) => {
      for (const c of list ?? []) {
        out.push({ id: c.id, x: c.x, y: c.y, w: c.width, h: c.height, node: cById.has(c.id) ? undefined : byId.get(c.id), container: cById.get(c.id) })
        walk(c.children)
      }
    }
    walk(res.children)
    // containers first so nodes paint on top
    out.sort((a, b) => Number(!!b.container) - Number(!!a.container))
    boxes.value = out
    paths.value = (res.edges ?? []).flatMap((e: any) => (e.sections ?? []).map((s: any, i: number) => {
      const pts = [s.startPoint, ...(s.bendPoints ?? []), s.endPoint]
      const l = e.labels?.[0]
      const src = drawn[Number(String(e.id).slice(1))]
      return { key: e.id + ':' + i, from: src?.from ?? '', to: src?.to ?? '', d: 'M' + pts.map((p: any) => `${p.x},${p.y}`).join(' L'), label: l?.text, lx: l?.x, ly: l?.y }
    }))
    size.value = { w: res.width, h: res.height }
    layoutError.value = ''
    fit()
  } catch (e) {
    if (mine === seq) layoutError.value = 'Layout failed: ' + (e instanceof Error ? e.message : String(e))
  }
}
// lay out again only when what is drawn changes shape; a change of look (fade,
// tag, selection) keeps the layout, the zoom and the pan as they are
const shape = computed(() => JSON.stringify([
  props.nodes.map((n) => [n.id, height(n)]), props.edges.map((e) => [e.from, e.to, e.label ?? '']),
  props.containers.map((c) => [c.id, c.label, c.children]), props.direction, props.packed, props.nodeWidth,
]))
watch(shape, layout)
// boxes keep the node they were laid out with; draw the current one
const byId = computed(() => new Map(props.nodes.map((n) => [n.id, n])))
const live = computed(() => boxes.value.map((b) => (b.node ? { ...b, node: byId.value.get(b.id) ?? b.node } : b)))
onMounted(layout)

// ---- pan / zoom ----
const host = ref<HTMLDivElement>()
const svg = ref<SVGSVGElement>()
const view = ref({ x: 0, y: 0, k: 1 })
function fit() {
  const el = host.value
  if (!el || !size.value.w) return
  const k = Math.min(1.2, (el.clientWidth - 16) / size.value.w, (el.clientHeight - 16) / size.value.h)
  view.value = { k, x: (el.clientWidth - size.value.w * k) / 2, y: Math.max(8, (el.clientHeight - size.value.h * k) / 2) }
}
function zoom(f: number, cx?: number, cy?: number) {
  const el = host.value!
  const px = cx ?? el.clientWidth / 2, py = cy ?? el.clientHeight / 2
  const k = Math.max(0.15, Math.min(3, view.value.k * f))
  view.value = { k, x: px - ((px - view.value.x) * k) / view.value.k, y: py - ((py - view.value.y) * k) / view.value.k }
}
function onWheel(e: WheelEvent) {
  if ((e.target as Element).closest('[data-ui]')) return
  e.preventDefault()
  const r = host.value!.getBoundingClientRect()
  zoom(e.deltaY < 0 ? 1.12 : 1 / 1.12, e.clientX - r.left, e.clientY - r.top)
}
let drag: { x: number; y: number; vx: number; vy: number } | null = null
function down(e: PointerEvent) {
  if ((e.target as Element).closest('[data-node],[data-ui]')) return
  drag = { x: e.clientX, y: e.clientY, vx: view.value.x, vy: view.value.y }
  ;(e.currentTarget as Element).setPointerCapture(e.pointerId)
}
function move(e: PointerEvent) {
  if (drag) view.value = { ...view.value, x: drag.vx + e.clientX - drag.x, y: drag.vy + e.clientY - drag.y }
}
function up() { drag = null }

// ---- export ----
function serialized(): string {
  const clone = svg.value!.cloneNode(true) as SVGSVGElement
  clone.setAttribute('xmlns', 'http://www.w3.org/2000/svg')
  clone.setAttribute('width', String(size.value.w))
  clone.setAttribute('height', String(size.value.h))
  clone.setAttribute('viewBox', `0 0 ${size.value.w} ${size.value.h}`)
  clone.querySelector('g[data-viewport]')?.removeAttribute('transform')
  return new XMLSerializer().serializeToString(clone)
}
function download(blob: Blob, name: string) {
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 1000)
}
function exportSVG(name = 'graph.svg') { download(new Blob([serialized()], { type: 'image/svg+xml' }), name) }
function exportPNG(name = 'graph.png') {
  const img = new Image()
  img.onload = () => {
    const c = document.createElement('canvas')
    c.width = size.value.w * 2
    c.height = size.value.h * 2
    const ctx = c.getContext('2d')!
    ctx.fillStyle = pal.value.canvas
    ctx.fillRect(0, 0, c.width, c.height)
    ctx.scale(2, 2)
    ctx.drawImage(img, 0, 0)
    c.toBlob((b) => b && download(b, name))
  }
  img.src = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(serialized())
}
// pan so a node sits in the middle, zooming in if it would be too small to read
function center(id: string) {
  const el = host.value
  const b = boxes.value.find((x) => x.id === id)
  if (!el || !b) return
  const k = Math.max(view.value.k, 0.8)
  view.value = { k, x: el.clientWidth / 2 - (b.x + b.w / 2) * k, y: el.clientHeight / 2 - (b.y + b.h / 2) * k }
}
defineExpose({ fit, zoom, center, exportSVG, exportPNG })

// ---- look ----
// Concrete colours (not var()) so an exported SVG/PNG looks the same outside
// the page; re-read from the tokens whenever the theme or accent changes.
const pal = computed(() => {
  void theme.value
  void accent.value
  const c = (n: string) => token('--' + n)
  return {
    canvas: c('bg-inset'), dot: c('line-strong'), edge: c('text-faint'), label: c('text-muted'), text: c('text'),
    box: c('line-hover'), selected: c('text'), focus: c('accent'), tint: c('hover-tint'), fail: c('fail'), warn: c('warn'), ok: c('ok'), idle: c('text-faint'),
    chip: {
      fill: c('bg-card'), stroke: c('line-strong'), warnFill: c('warn-bg'), warnStroke: c('warn-line'), well: c('bg-inset'),
      okFill: c('ok-bg'), okStroke: c('ok-line'),
    },
    style: {
      '': { stroke: c('line-hover'), fill: c('bg-panel') },
      create: { stroke: c('ok'), fill: c('ok-bg'), tag: '+ create', tagColor: c('ok') },
      update: { stroke: c('running'), fill: c('running-bg'), tag: '~ update', tagColor: c('running') },
      replace: { stroke: c('fail'), fill: c('fail-bg'), tag: '± replace', tagColor: c('fail') },
      delete: { stroke: c('fail'), fill: c('fail-bg'), tag: '− destroy', tagColor: c('fail') },
      drift: { stroke: c('warn'), fill: c('warn-bg'), dash: '6 4', tag: 'drift', tagColor: c('warn') },
    } as Record<string, { stroke: string; fill: string; dash?: string; tag?: string; tagColor?: string }>,
  }
})
const look = (n: GNode) => {
  const s = pal.value.style[n.state ?? ''] ?? pal.value.style['']
  return n.errors ? { ...s, stroke: pal.value.fail, dash: '5 3' } : s
}
const trunc = (s: string, n: number) => (s.length > n ? s.slice(0, n - 1) + '…' : s)
const chars = (px: number) => Math.max(4, Math.floor(px / CH))
// characters that fit a 210px-wide node, scaled to this node's width
const fitW = (b: Box, at210: number) => Math.floor((at210 * b.w) / 210)
// chips right-aligned on a row; the row's text (starting `left` px in) gets the room that's left
interface PlacedChip extends GChip { x: number; w: number }
function placeChips(b: Box, r: GNodeRow, left = 14): { chips: PlacedChip[]; textChars: number } {
  let x = b.x + b.w - 14
  const chips: PlacedChip[] = []
  for (const c of [...(r.chips ?? [])].reverse()) {
    const w = c.text.length * CH + 10
    if (x - w < b.x + 14 + 4 * CH) break // no room: drop the rest (the inspector lists them)
    x -= w
    chips.unshift({ ...c, x, w })
    x -= 4
  }
  return { chips, textChars: chars(x - (b.x + left + 6)) }
}
const chipFill = (c: GChip) => (c.tone === 'warn' ? pal.value.chip.warnFill : c.tone === 'ok' ? pal.value.chip.okFill : pal.value.chip.fill)
const chipStroke = (c: GChip) => (c.tone === 'warn' ? pal.value.chip.warnStroke : c.tone === 'ok' ? pal.value.chip.okStroke : pal.value.chip.stroke)
const chipText = (c: GChip) => (c.tone === 'warn' ? pal.value.warn : c.tone === 'ok' ? pal.value.ok : c.tone === 'muted' ? pal.value.label : pal.value.text)
const rowText = (r: GNodeRow) => (r.tone === 'warn' ? pal.value.warn : r.tone === 'muted' ? pal.value.label : pal.value.text)
const dotColor = (d?: GCard['dot']) => (d === 'ok' ? pal.value.ok : d === 'warn' ? pal.value.warn : pal.value.idle)
const hl = computed(() => new Set(props.highlight))
const lit = computed(() => new Set(props.reach))
const litEdges = computed(() => new Set(props.reachEdges))
const fadeOf = computed(() => new Map(props.nodes.map((n) => [n.id, n.fade ?? (n.dim ? 0.45 : 1)])))
// an edge is as faint as its fainter end; with a selection, only its paths stay
function edgeOpacity(p: Path) {
  if (litEdges.value.size) return litEdges.value.has(p.from + '>' + p.to) ? 1 : 0.15
  return Math.min(fadeOf.value.get(p.from) ?? 1, fadeOf.value.get(p.to) ?? 1)
}
const summary = computed(() => `${props.nodes.length} nodes, ${props.edges.length} edges`)
</script>

<template>
  <div ref="host" class="canvas gridbg" @wheel="onWheel" @pointerdown="down" @pointermove="move" @pointerup="up">
    <p v-if="layoutError" class="err pad">{{ layoutError }}</p>
    <p v-else-if="!nodes.length" class="muted pad">Nothing to draw.</p>
    <svg ref="svg" class="svg" role="group" :aria-label="`${ariaLabel}: ${summary}`" font-family="JetBrains Mono, ui-monospace, monospace">
      <defs>
        <marker id="gw-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L8,4 L0,8 z" :fill="pal.edge" />
        </marker>
        <marker id="gw-arrow-lit" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L8,4 L0,8 z" :fill="pal.focus" />
        </marker>
      </defs>
      <g data-viewport :transform="`translate(${view.x},${view.y}) scale(${view.k})`">
        <rect :width="size.w" :height="size.h" :fill="pal.canvas" opacity="0" />
        <template v-for="b in live" :key="'c' + b.id">
          <g v-if="b.container" data-node class="node" role="button" tabindex="0" :aria-label="`${b.container.label} (contains ${b.container.children.length})`"
            @click="emit('select', b.id)" @keydown.enter="emit('select', b.id)">
            <rect :x="b.x" :y="b.container.group ? b.y + 24 : b.y" :width="b.w" :height="b.container.group ? b.h - 24 : b.h" rx="12" :fill="pal.tint" :data-focus="b.id === focus || undefined"
              :stroke="b.id === focus ? pal.focus : b.id === selected ? pal.selected : pal.box" :stroke-dasharray="b.container.group ? '3 4' : '6 5'" :stroke-width="b.container.group ? 1.5 : 1" />
            <text :x="b.x + (b.container.group ? 4 : 16)" :y="b.y + (b.container.group ? 15 : 24)" font-size="12" :font-weight="b.container.group ? 600 : 400" :fill="pal.label">{{ b.container.label }}</text>
          </g>
        </template>
        <g v-for="p in paths" :key="p.key" :opacity="edgeOpacity(p)" :data-lit="litEdges.has(p.from + '>' + p.to) || undefined">
          <path :d="p.d" fill="none" :stroke="litEdges.has(p.from + '>' + p.to) ? pal.focus : pal.edge" :stroke-width="litEdges.has(p.from + '>' + p.to) ? 2.2 : 1.5"
            :marker-end="litEdges.has(p.from + '>' + p.to) ? 'url(#gw-arrow-lit)' : 'url(#gw-arrow)'" />
          <text v-if="p.label" :x="p.lx! + 4" :y="p.ly! + 11" font-size="10.5" :fill="pal.label">{{ p.label }}</text>
        </g>
        <template v-for="b in live" :key="'n' + b.id">
          <g v-if="b.node" data-node :data-id="b.id" class="node" role="button" tabindex="0"
            :aria-label="`${b.node.title} ${b.node.label}${b.node.state ? ', ' + b.node.state : ''}${b.node.note ? ', ' + b.node.note : ''}`"
            :opacity="fadeOf.get(b.id) ?? 1" @click="emit('select', b.id)" @keydown.enter="emit('select', b.id)">
            <text v-if="b.node.tag" :x="b.x + 2" :y="b.y - 7" font-size="10.5" font-weight="600" :fill="pal.focus">{{ b.node.tag }}</text>
            <rect :x="b.x" :y="b.y" :width="b.w" :height="b.h" rx="8" :fill="look(b.node).fill" :stroke="look(b.node).stroke"
              :stroke-dasharray="look(b.node).dash" stroke-width="1.2" />
            <rect v-if="b.id === selected || b.id === focus || hl.has(b.id) || lit.has(b.id)" :x="b.x - 4" :y="b.y - 4" :width="b.w + 8" :height="b.h + 8" rx="11"
              fill="none" :data-focus="b.id === focus || undefined" :stroke="b.id === focus || (lit.has(b.id) && b.id !== selected) ? pal.focus : pal.selected"
              :stroke-opacity="lit.has(b.id) && b.id !== selected ? 0.7 : 0.8" :stroke-width="lit.has(b.id) && b.id !== selected ? 1.5 : 2" />
            <text :x="b.x + 14" :y="b.y + 24" font-size="11" :fill="look(b.node).tagColor ?? pal.label">{{ trunc(b.node.title + (look(b.node).tag ? ' · ' + look(b.node).tag : ''), fitW(b, 30)) }}</text>
            <text :x="b.x + 14" :y="b.y + 44" font-size="13" font-weight="700" :fill="pal.text">{{ trunc(b.node.label, fitW(b, 22) - (b.node.star ? 2 : 0) - (b.node.meta ? Math.ceil((b.node.meta.length + 2) * 11 / 13) : 0)) }}<tspan v-if="b.node.star" :fill="pal.focus"> *</tspan><tspan v-if="b.node.count" :fill="pal.label" font-weight="400"> {{ b.node.count }}</tspan></text>
            <text v-if="b.node.meta" :x="b.x + b.w - 12" :y="b.y + 44" font-size="11" text-anchor="end" :fill="pal.label">{{ b.node.meta }}</text>
            <text v-if="b.node.errors" :x="b.x + b.w - 12" :y="b.y + 20" font-size="11" text-anchor="end" :fill="pal.fail">! {{ b.node.errors }}</text>
            <template v-if="b.node.rows?.length">
              <line :x1="b.x + 10" :x2="b.x + b.w - 10" :y1="b.y + NH - 2" :y2="b.y + NH - 2" :stroke="look(b.node).stroke" stroke-opacity="0.5" />
              <g v-for="(l, i) in rowsOf(b.node)" :key="i">
                <template v-if="l.r.card">
                  <rect :x="b.x + 8" :y="b.y + l.y" :width="b.w - 16" :height="l.h - 4" rx="6" :fill="pal.chip.fill"
                    :stroke="l.r.card.dot === 'warn' ? pal.chip.warnStroke : pal.chip.stroke" :stroke-dasharray="l.r.card.dashed ? '4 3' : undefined" />
                  <circle :cx="b.x + 18" :cy="b.y + l.y + 10" r="3.5" :fill="dotColor(l.r.card.dot)" />
                  <text :x="b.x + 27" :y="b.y + l.y + 14" font-size="11" font-weight="600" :fill="rowText(l.r)">{{ trunc(l.r.text, placeChips(b, l.r, 27).textChars) }}</text>
                  <g v-for="(c, j) in placeChips(b, l.r, 27).chips" :key="j">
                    <rect :x="c.x" :y="b.y + l.y + 3" :width="c.w" height="15" rx="3" :fill="chipFill(c)" :stroke="chipStroke(c)" :stroke-dasharray="c.tone === 'muted' ? '2 2' : undefined" />
                    <text :x="c.x + 5" :y="b.y + l.y + 14" font-size="11" :fill="chipText(c)">{{ c.text }}</text>
                  </g>
                  <text v-if="l.r.card.sub" :x="b.x + 18" :y="b.y + l.y + 28" font-size="10.5" :fill="l.r.card.subTone === 'warn' ? pal.warn : pal.label">{{ trunc(l.r.card.sub, Math.floor((b.w - 30) / 6.3)) }}</text>
                </template>
                <template v-else-if="l.r.chips?.length">
                  <rect :x="b.x + 8" :y="b.y + l.y" :width="b.w - 16" :height="l.h - 3" rx="5" :fill="pal.chip.well" />
                  <text :x="b.x + 14" :y="b.y + l.y + 13" font-size="11" :fill="rowText(l.r)">{{ trunc(l.r.text, placeChips(b, l.r).textChars) }}</text>
                  <g v-for="(c, j) in placeChips(b, l.r).chips" :key="j">
                    <rect :x="c.x" :y="b.y + l.y + 2" :width="c.w" height="15" rx="3" :fill="chipFill(c)" :stroke="chipStroke(c)" :stroke-dasharray="c.tone === 'muted' ? '2 2' : undefined" />
                    <text :x="c.x + 5" :y="b.y + l.y + 13" font-size="11" :fill="chipText(c)">{{ c.text }}</text>
                  </g>
                </template>
                <text v-else :x="b.x + 14" :y="b.y + l.y + 11" font-size="11" :fill="rowText(l.r)">{{ trunc(l.r.text, fitW(b, 30)) }}</text>
              </g>
            </template>
          </g>
        </template>
      </g>
    </svg>
    <div v-if="controls" class="ctl" data-ui>
      <button class="btn sq" type="button" aria-label="Zoom in" title="Zoom in" @click="zoom(1.2)">+</button>
      <button class="btn sq" type="button" aria-label="Zoom out" title="Zoom out" @click="zoom(1 / 1.2)">−</button>
      <button class="btn sq" type="button" aria-label="Fit to view" title="Fit to view" @click="fit()">⤢</button>
    </div>
    <div v-if="$slots.legend" class="legend" data-ui><slot name="legend" /></div>
  </div>
</template>

<style scoped>
.canvas { position: relative; width: 100%; height: 100%; min-height: 320px; overflow: hidden; cursor: grab; touch-action: none; }
.canvas:active { cursor: grabbing; }
.gridbg { background-color: var(--bg-inset); background-image: radial-gradient(var(--line-strong) 1px, transparent 1px); background-size: 20px 20px; }
.svg { position: absolute; inset: 0; width: 100%; height: 100%; }
.node { cursor: pointer; outline: none; }
.node:focus-visible rect:first-child { stroke: var(--accent); stroke-width: 2; }
.pad { position: absolute; padding: 16px; margin: 0; }
.ctl { position: absolute; top: 10px; right: 10px; display: flex; gap: 4px; cursor: default; }
.btn.sq { height: 28px; width: 28px; padding: 0; justify-content: center; background: var(--bg-card); }
.legend { position: absolute; left: 10px; bottom: 10px; max-width: calc(100% - 20px); padding: 8px 10px; border: 1px solid var(--line-strong); border-radius: 8px;
  background: var(--bg-card); box-shadow: var(--shadow); font-size: 12px; color: var(--text-muted); cursor: default; }
</style>
