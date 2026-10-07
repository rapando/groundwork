<script setup lang="ts">
import { computed, onMounted, ref, shallowRef, watch } from 'vue'

export interface GNode { id: string; title: string; label: string; count?: string; state?: string; errors?: number; dim?: boolean }
export interface GEdge { from: string; to: string; label?: string }
export interface GContainer { id: string; label: string; children: string[] }

const props = withDefaults(defineProps<{
  nodes: GNode[]; edges: GEdge[]; containers?: GContainer[]
  selected?: string; focus?: string; direction?: 'RIGHT' | 'DOWN'; ariaLabel?: string
}>(), { containers: () => [], selected: '', focus: '', direction: 'RIGHT', ariaLabel: 'Graph' })
const emit = defineEmits<{ (e: 'select', id: string): void }>()

const NW = 210, NH = 60
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
  const leaf = (n: GNode) => ({ id: n.id, width: NW, height: NH })
  const children: any[] = []
  for (const c of props.containers) {
    children.push({
      id: c.id,
      layoutOptions: { 'elk.padding': '[top=44,left=20,bottom=20,right=20]' },
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
      layoutOptions: {
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
    ctx.fillStyle = '#0A0C0E'
    ctx.fillRect(0, 0, c.width, c.height)
    ctx.scale(2, 2)
    ctx.drawImage(img, 0, 0)
    c.toBlob((b) => b && download(b, name))
  }
  img.src = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(serialized())
}
defineExpose({ fit, zoom, exportSVG, exportPNG })

// ---- look ----
const STYLE: Record<string, { stroke: string; fill: string; dash?: string; tag?: string; tagColor?: string }> = {
  '': { stroke: '#3A444B', fill: '#12161A' },
  create: { stroke: '#5FD38D', fill: '#101A14', tag: '+ create', tagColor: '#5FD38D' },
  update: { stroke: '#7CC0FF', fill: '#121A22', tag: '~ update', tagColor: '#7CC0FF' },
  replace: { stroke: '#FF7A7A', fill: '#1A1213', tag: '± replace', tagColor: '#FF7A7A' },
  delete: { stroke: '#FF7A7A', fill: '#1A1213', tag: '− destroy', tagColor: '#FF7A7A' },
  drift: { stroke: '#F5B647', fill: '#1A1610', dash: '6 4', tag: 'drift', tagColor: '#F5B647' },
}
const look = (n: GNode) => {
  const s = STYLE[n.state ?? ''] ?? STYLE['']
  return n.errors ? { ...s, stroke: '#FF7A7A', dash: '5 3' } : s
}
const trunc = (s: string, n: number) => (s.length > n ? s.slice(0, n - 1) + '…' : s)
const summary = computed(() => `${props.nodes.length} nodes, ${props.edges.length} edges`)
</script>

<template>
  <div ref="host" class="canvas gridbg" @wheel="onWheel" @pointerdown="down" @pointermove="move" @pointerup="up">
    <p v-if="layoutError" class="err pad">{{ layoutError }}</p>
    <p v-else-if="!nodes.length" class="muted pad">Nothing to draw.</p>
    <svg ref="svg" class="svg" role="img" :aria-label="`${ariaLabel}: ${summary}`" font-family="JetBrains Mono, ui-monospace, monospace">
      <defs>
        <marker id="gw-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L8,4 L0,8 z" fill="#5B666D" />
        </marker>
      </defs>
      <g data-viewport :transform="`translate(${view.x},${view.y}) scale(${view.k})`">
        <rect :width="size.w" :height="size.h" fill="#0A0C0E" opacity="0" />
        <template v-for="b in boxes" :key="'c' + b.id">
          <g v-if="b.container" data-node class="node" role="button" tabindex="0" :aria-label="`${b.container.label} (contains ${b.container.children.length})`"
            @click="emit('select', b.id)" @keydown.enter="emit('select', b.id)">
            <rect :x="b.x" :y="b.y" :width="b.w" :height="b.h" rx="12" fill="rgba(255,255,255,0.015)"
              :stroke="b.id === focus ? '#B8F36B' : b.id === selected ? '#E6E9EB' : '#3A444B'" stroke-dasharray="6 5" />
            <text :x="b.x + 16" :y="b.y + 24" font-size="12" fill="#8B959C">{{ b.container.label }}</text>
          </g>
        </template>
        <g v-for="p in paths" :key="p.key">
          <path :d="p.d" fill="none" stroke="#5B666D" stroke-width="1.5" marker-end="url(#gw-arrow)" />
          <text v-if="p.label" :x="p.lx! + 4" :y="p.ly! + 11" font-size="10.5" fill="#8B959C">{{ p.label }}</text>
        </g>
        <template v-for="b in boxes" :key="'n' + b.id">
          <g v-if="b.node" data-node :data-id="b.id" class="node" role="button" tabindex="0" :aria-label="`${b.node.title} ${b.node.label}${b.node.state ? ', ' + b.node.state : ''}`"
            :opacity="b.node.dim ? 0.45 : 1" @click="emit('select', b.id)" @keydown.enter="emit('select', b.id)">
            <rect :x="b.x" :y="b.y" :width="b.w" :height="b.h" rx="8" :fill="look(b.node).fill" :stroke="look(b.node).stroke"
              :stroke-dasharray="look(b.node).dash" stroke-width="1.2" />
            <rect v-if="b.id === selected || b.id === focus" :x="b.x - 4" :y="b.y - 4" :width="b.w + 8" :height="b.h + 8" rx="11"
              fill="none" :stroke="b.id === focus ? '#B8F36B' : '#E6E9EB'" stroke-opacity="0.8" stroke-width="2" />
            <text :x="b.x + 14" :y="b.y + 24" font-size="11" :fill="look(b.node).tagColor ?? '#8B959C'">{{ trunc(b.node.title + (look(b.node).tag ? ' · ' + look(b.node).tag : ''), 30) }}</text>
            <text :x="b.x + 14" :y="b.y + 44" font-size="13" font-weight="700" fill="#E6E9EB">{{ trunc(b.node.label, 22) }}<tspan v-if="b.node.count" fill="#8B959C" font-weight="400"> {{ b.node.count }}</tspan></text>
            <text v-if="b.node.errors" :x="b.x + b.w - 12" :y="b.y + 20" font-size="11" text-anchor="end" fill="#FF7A7A">! {{ b.node.errors }}</text>
          </g>
        </template>
      </g>
    </svg>
  </div>
</template>

<style scoped>
.canvas { position: relative; width: 100%; height: 100%; min-height: 320px; overflow: hidden; cursor: grab; touch-action: none; }
.canvas:active { cursor: grabbing; }
.gridbg { background-color: #0A0C0E; background-image: radial-gradient(#1C2226 1px, transparent 1px); background-size: 20px 20px; }
.svg { position: absolute; inset: 0; width: 100%; height: 100%; }
.node { cursor: pointer; outline: none; }
.node:focus-visible rect:first-child { stroke: #B8F36B; stroke-width: 2; }
.pad { position: absolute; padding: 16px; margin: 0; }
</style>
