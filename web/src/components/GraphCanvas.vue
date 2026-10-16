<script setup lang="ts">
import { computed, onMounted, ref, shallowRef, watch } from 'vue'
import { accent, theme, token } from '../composables/theme'

// rows: extra lines under the label (the system view lists services, with
// their ports as chips at the right); star marks a node drawn more than once
export interface GChip { text: string; tone?: 'warn' | 'muted' }
export interface GNodeRow { text: string; tone?: 'warn' | 'muted'; chips?: GChip[] }
export interface GNode { id: string; title: string; label: string; count?: string; state?: string; errors?: number; dim?: boolean; star?: boolean; rows?: GNodeRow[] }
export interface GEdge { from: string; to: string; label?: string }
// group: a dashed frame with its label above it (an inventory group)
export interface GContainer { id: string; label: string; children: string[]; group?: boolean }

const props = withDefaults(defineProps<{
  nodes: GNode[]; edges: GEdge[]; containers?: GContainer[]
  selected?: string; focus?: string; direction?: 'RIGHT' | 'DOWN'; ariaLabel?: string
  // packed: boxes are packed into rows (no edges to route), groups included
  packed?: boolean; nodeWidth?: number; highlight?: string[]
}>(), { containers: () => [], selected: '', focus: '', direction: 'RIGHT', ariaLabel: 'Graph', packed: false, nodeWidth: 210, highlight: () => [] })
const emit = defineEmits<{ (e: 'select', id: string): void }>()

const NH = 60, ROW = 17, CHIP_ROW = 22, CH = 6.6 // monospace 11px advance
const rowH = (n: GNode) => (n.rows?.some((r) => r.chips?.length) ? CHIP_ROW : ROW)
const height = (n: GNode) => (n.rows?.length ? NH + 8 + n.rows.length * rowH(n) : NH)
interface Box { id: string; x: number; y: number; w: number; h: number; node?: GNode; container?: GContainer }
interface Path { d: string; label?: string; lx?: number; ly?: number; key: string }
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
  const edges = props.edges
    .filter((e) => visible.has(e.from) && visible.has(e.to) && e.from !== e.to)
    .filter((e) => !(containerIds.has(e.from) && containerOf.get(e.to) === e.from))
    .map((e, i) => ({
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
      return { key: e.id + ':' + i, d: 'M' + pts.map((p: any) => `${p.x},${p.y}`).join(' L'), label: l?.text, lx: l?.x, ly: l?.y }
    }))
    size.value = { w: res.width, h: res.height }
    layoutError.value = ''
    fit()
  } catch (e) {
    if (mine === seq) layoutError.value = 'Layout failed: ' + (e instanceof Error ? e.message : String(e))
  }
}
watch(() => [props.nodes, props.edges, props.containers, props.direction], layout, { deep: true })
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
  e.preventDefault()
  const r = host.value!.getBoundingClientRect()
  zoom(e.deltaY < 0 ? 1.12 : 1 / 1.12, e.clientX - r.left, e.clientY - r.top)
}
let drag: { x: number; y: number; vx: number; vy: number } | null = null
function down(e: PointerEvent) {
  if ((e.target as Element).closest('[data-node]')) return
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
defineExpose({ fit, zoom, exportSVG, exportPNG })

// ---- look ----
// Concrete colours (not var()) so an exported SVG/PNG looks the same outside
// the page; re-read from the tokens whenever the theme or accent changes.
const pal = computed(() => {
  void theme.value
  void accent.value
  const c = (n: string) => token('--' + n)
  return {
    canvas: c('bg-inset'), dot: c('line-strong'), edge: c('text-faint'), label: c('text-muted'), text: c('text'),
    box: c('line-hover'), selected: c('text'), focus: c('accent'), tint: c('hover-tint'), fail: c('fail'), warn: c('warn'),
    chip: { fill: c('bg-card'), stroke: c('line-strong'), warnFill: c('warn-bg'), warnStroke: c('warn-line'), well: c('bg-inset') },
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
// chips right-aligned on a row; the row's text gets the room that's left
interface PlacedChip extends GChip { x: number; w: number }
function placeChips(b: Box, r: GNodeRow): { chips: PlacedChip[]; textChars: number } {
  let x = b.x + b.w - 14
  const chips: PlacedChip[] = []
  for (const c of [...(r.chips ?? [])].reverse()) {
    const w = c.text.length * CH + 10
    if (x - w < b.x + 14 + 4 * CH) break // no room: drop the rest (the inspector lists them)
    x -= w
    chips.unshift({ ...c, x, w })
    x -= 4
  }
  return { chips, textChars: chars(x - (b.x + 20)) }
}
const hl = computed(() => new Set(props.highlight))
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
      </defs>
      <g data-viewport :transform="`translate(${view.x},${view.y}) scale(${view.k})`">
        <rect :width="size.w" :height="size.h" :fill="pal.canvas" opacity="0" />
        <template v-for="b in boxes" :key="'c' + b.id">
          <g v-if="b.container" data-node class="node" role="button" tabindex="0" :aria-label="`${b.container.label} (contains ${b.container.children.length})`"
            @click="emit('select', b.id)" @keydown.enter="emit('select', b.id)">
            <rect :x="b.x" :y="b.container.group ? b.y + 24 : b.y" :width="b.w" :height="b.container.group ? b.h - 24 : b.h" rx="12" :fill="pal.tint" :data-focus="b.id === focus || undefined"
              :stroke="b.id === focus ? pal.focus : b.id === selected ? pal.selected : pal.box" :stroke-dasharray="b.container.group ? '3 4' : '6 5'" :stroke-width="b.container.group ? 1.5 : 1" />
            <text :x="b.x + (b.container.group ? 4 : 16)" :y="b.y + (b.container.group ? 15 : 24)" font-size="12" :font-weight="b.container.group ? 600 : 400" :fill="pal.label">{{ b.container.label }}</text>
          </g>
        </template>
        <g v-for="p in paths" :key="p.key">
          <path :d="p.d" fill="none" :stroke="pal.edge" stroke-width="1.5" marker-end="url(#gw-arrow)" />
          <text v-if="p.label" :x="p.lx! + 4" :y="p.ly! + 11" font-size="10.5" :fill="pal.label">{{ p.label }}</text>
        </g>
        <template v-for="b in boxes" :key="'n' + b.id">
          <g v-if="b.node" data-node :data-id="b.id" class="node" role="button" tabindex="0" :aria-label="`${b.node.title} ${b.node.label}${b.node.state ? ', ' + b.node.state : ''}`"
            :opacity="b.node.dim ? 0.45 : 1" @click="emit('select', b.id)" @keydown.enter="emit('select', b.id)">
            <rect :x="b.x" :y="b.y" :width="b.w" :height="b.h" rx="8" :fill="look(b.node).fill" :stroke="look(b.node).stroke"
              :stroke-dasharray="look(b.node).dash" stroke-width="1.2" />
            <rect v-if="b.id === selected || b.id === focus || hl.has(b.id)" :x="b.x - 4" :y="b.y - 4" :width="b.w + 8" :height="b.h + 8" rx="11"
              fill="none" :data-focus="b.id === focus || undefined" :stroke="b.id === focus ? pal.focus : pal.selected" stroke-opacity="0.8" stroke-width="2" />
            <text :x="b.x + 14" :y="b.y + 24" font-size="11" :fill="look(b.node).tagColor ?? pal.label">{{ trunc(b.node.title + (look(b.node).tag ? ' · ' + look(b.node).tag : ''), fitW(b, 30)) }}</text>
            <text :x="b.x + 14" :y="b.y + 44" font-size="13" font-weight="700" :fill="pal.text">{{ trunc(b.node.label, fitW(b, 22) - (b.node.star ? 2 : 0)) }}<tspan v-if="b.node.star" :fill="pal.focus"> *</tspan><tspan v-if="b.node.count" :fill="pal.label" font-weight="400"> {{ b.node.count }}</tspan></text>
            <text v-if="b.node.errors" :x="b.x + b.w - 12" :y="b.y + 20" font-size="11" text-anchor="end" :fill="pal.fail">! {{ b.node.errors }}</text>
            <template v-if="b.node.rows?.length">
              <line :x1="b.x + 10" :x2="b.x + b.w - 10" :y1="b.y + NH - 2" :y2="b.y + NH - 2" :stroke="look(b.node).stroke" stroke-opacity="0.5" />
              <template v-if="rowH(b.node) === CHIP_ROW">
                <g v-for="(r, i) in b.node.rows" :key="i">
                  <rect :x="b.x + 8" :y="b.y + NH + 3 + i * CHIP_ROW" :width="b.w - 16" :height="CHIP_ROW - 3" rx="5" :fill="pal.chip.well" />
                  <text :x="b.x + 14" :y="b.y + NH + 16 + i * CHIP_ROW" font-size="11"
                    :fill="r.tone === 'warn' ? pal.warn : r.tone === 'muted' ? pal.label : pal.text">{{ trunc(r.text, placeChips(b, r).textChars) }}</text>
                  <g v-for="(c, j) in placeChips(b, r).chips" :key="j">
                    <rect :x="c.x" :y="b.y + NH + 5 + i * CHIP_ROW" :width="c.w" :height="CHIP_ROW - 7" rx="3"
                      :fill="c.tone === 'warn' ? pal.chip.warnFill : pal.chip.fill" :stroke="c.tone === 'warn' ? pal.chip.warnStroke : pal.chip.stroke" :stroke-dasharray="c.tone === 'muted' ? '2 2' : undefined" />
                    <text :x="c.x + 5" :y="b.y + NH + 16 + i * CHIP_ROW" font-size="11" :fill="c.tone === 'warn' ? pal.warn : c.tone === 'muted' ? pal.label : pal.text">{{ c.text }}</text>
                  </g>
                </g>
              </template>
              <template v-else>
                <text v-for="(r, i) in b.node.rows" :key="i" :x="b.x + 14" :y="b.y + NH + 13 + i * ROW" font-size="11"
                  :fill="r.tone === 'warn' ? pal.warn : r.tone === 'muted' ? pal.label : pal.text">{{ trunc(r.text, fitW(b, 30)) }}</text>
              </template>
            </template>
          </g>
        </template>
      </g>
    </svg>
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
</style>
