import type { GraphEdge, GraphNode } from '../api/types'

// Edges point from a dependency to what uses it (from → to).

export const edgeKey = (e: { from: string; to: string }) => e.from + '>' + e.to

/** What a node reaches: everything it uses (upstream) and everything that uses it (downstream), directly or not. */
export interface Reach {
  up: Set<string> // nodes it uses, directly or not
  down: Set<string> // nodes that use it, directly or not
  directUp: Set<string>
  directDown: Set<string>
  edges: Set<string> // edgeKey of every edge on a path to or from it
}

function walk(start: string, next: Map<string, string[]>): Set<string> {
  const seen = new Set<string>()
  const queue = [start]
  while (queue.length) {
    for (const n of next.get(queue.shift()!) ?? []) {
      if (n === start || seen.has(n)) continue
      seen.add(n)
      queue.push(n)
    }
  }
  return seen
}

export function reach(edges: GraphEdge[], id: string): Reach {
  const fwd = new Map<string, string[]>()
  const back = new Map<string, string[]>()
  for (const e of edges) {
    if (e.from === e.to) continue
    fwd.set(e.from, [...(fwd.get(e.from) ?? []), e.to])
    back.set(e.to, [...(back.get(e.to) ?? []), e.from])
  }
  const up = walk(id, back)
  const down = walk(id, fwd)
  const onUp = (n: string) => n === id || up.has(n)
  const onDown = (n: string) => n === id || down.has(n)
  const keys = new Set<string>()
  for (const e of edges) {
    // an edge between two nodes that both reach the selection lies on a path to it (and likewise downstream)
    if ((up.has(e.from) && onUp(e.to)) || (onDown(e.from) && down.has(e.to))) keys.add(edgeKey(e))
  }
  return { up, down, directUp: new Set(back.get(id) ?? []), directDown: new Set(fwd.get(id) ?? []), edges: keys }
}

export type Filter = 'all' | 'plan' | 'drifted' | 'errors'
export const FILTERS: { id: Filter; label: string }[] = [
  { id: 'all', label: 'All' },
  { id: 'plan', label: 'In plan' },
  { id: 'drifted', label: 'Drifted' },
  { id: 'errors', label: 'Has errors' },
]
export const asFilter = (v: unknown): Filter => (['plan', 'drifted', 'errors'].includes(String(v)) ? (v as Filter) : 'all')

export function inFilter(n: GraphNode, f: Filter): boolean {
  if (f === 'plan') return !!n.action
  if (f === 'drifted') return !!n.drift
  if (f === 'errors') return (n.errors ?? 0) > 0
  return true
}

export function filterCounts(nodes: GraphNode[]): Record<Filter, number> {
  const out: Record<Filter, number> = { all: 0, plan: 0, drifted: 0, errors: 0 }
  for (const n of nodes) for (const f of FILTERS) if (inFilter(n, f.id)) out[f.id]++
  return out
}

/** Whether a node matches a search: its address, type, name or file contains every word (any case). */
export function matchesQuery(n: GraphNode, q: string): boolean {
  const words = q.toLowerCase().split(/\s+/).filter(Boolean)
  if (!words.length) return true
  const hay = `${n.id} ${n.type} ${n.name} ${n.file}`.toLowerCase()
  return words.every((w) => hay.includes(w))
}

/** The nodes that pass the filter and the search, best search matches first (address starts with the query). */
export function findNodes(nodes: GraphNode[], f: Filter, q: string): GraphNode[] {
  const lq = q.trim().toLowerCase()
  const out = nodes.filter((n) => inFilter(n, f) && matchesQuery(n, q))
  if (!lq) return out
  const rank = (n: GraphNode) => (n.id.toLowerCase() === lq ? 0 : n.id.toLowerCase().startsWith(lq) || n.name.toLowerCase().startsWith(lq) ? 1 : 2)
  return out.sort((a, b) => rank(a) - rank(b))
}

export const FADE = { match: 1, near: 0.4, rest: 0.12 }

/**
 * Opacity per node so matches stand out without moving anything: matches are
 * drawn as usual, their direct neighbours fade, the rest almost disappear.
 * Returns null when everything matches (nothing to fade).
 */
export function fadeMap(all: string[], matches: Set<string>, edges: GraphEdge[]): Map<string, number> | null {
  if (matches.size === all.length) return null
  const out = new Map<string, number>()
  for (const id of all) out.set(id, matches.has(id) ? FADE.match : FADE.rest)
  for (const e of edges) {
    if (matches.has(e.from) && !matches.has(e.to) && out.has(e.to)) out.set(e.to, FADE.near)
    if (matches.has(e.to) && !matches.has(e.from) && out.has(e.from)) out.set(e.from, FADE.near)
  }
  return out
}

/** Nodes downstream of a selection that the overlaid plan replaces or destroys. */
export function riskyDownstream(nodes: GraphNode[], down: Set<string>): GraphNode[] {
  return nodes.filter((n) => down.has(n.id) && (n.action === 'replace' || n.action === 'delete'))
}
