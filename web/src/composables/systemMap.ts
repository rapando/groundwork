import type { GChip, GContainer, GNode, GNodeRow } from '../components/GraphCanvas.vue'
import type { PortRow, SystemHost, SystemHostBase, SystemModel } from '../api/types'

/** A host as the system map draws it, placed on a Terraform server or not. */
export interface MapHost {
  key: string // project/name
  host: SystemHostBase & { ports: PortRow[] }
  linked?: SystemHost
  reason?: string // why it isn't placed
}

export interface SystemMap {
  hosts: MapHost[]
  groups: number
  nodes: GNode[]
  containers: GContainer[]
  hostOf: Map<string, string> // node id → host key
}

export const portLabel = (p: { port: number; proto: string }) => (p.proto === 'tcp' ? String(p.port) : `${p.port}/${p.proto}`)
const bad = (p: PortRow) => p.flag === 'blocked' || p.flag === 'not-listening'
const chip = (p: PortRow, muted = false): GChip => ({ text: portLabel(p), tone: bad(p) ? 'warn' : muted ? 'muted' : undefined })

/** Services on a host (its roles first, then whatever else uses a port), each with its ports. */
export function serviceRows(h: MapHost['host']): GNodeRow[] {
  const byService = new Map<string, PortRow[]>()
  for (const r of h.roles) byService.set(r.name, [])
  const loose: PortRow[] = []
  for (const p of h.ports) {
    if (!p.services?.length) {
      if (p.flag === 'unused') loose.push(p)
      continue
    }
    for (const s of p.services) byService.set(s, [...(byService.get(s) ?? []), p])
  }
  const rows: GNodeRow[] = [...byService].map(([s, ports]) => ({ text: s, chips: ports.map((p) => chip(p)) }))
  if (loose.length) rows.push({ text: 'open', tone: 'muted', chips: loose.map((p) => chip(p, true)) })
  if (!rows.length) rows.push({ text: 'no roles or ports found', tone: 'muted' })
  return rows
}

/**
 * One box per host in each inventory group that lists it, inside a frame per
 * group; a host in several groups is drawn in each and starred. Hosts with no
 * group go in a last "ungrouped" frame.
 */
export function buildSystemMap(sys: SystemModel | null): SystemMap {
  const hosts: MapHost[] = []
  for (const l of Object.values(sys?.servers ?? {})) for (const h of l) hosts.push({ key: h.project + '/' + h.name, host: h, linked: h })
  for (const u of sys?.unlinked ?? []) hosts.push({ key: u.project + '/' + u.name, host: u, reason: u.reason })
  hosts.sort((a, b) => a.host.name.localeCompare(b.host.name) || a.host.project.localeCompare(b.host.project))

  const manyProjects = new Set(hosts.map((h) => h.host.project)).size > 1
  const frames = new Map<string, { label: string; sort: string; members: MapHost[] }>()
  const UNGROUPED = '￿'
  for (const h of hosts) {
    const gs = h.host.groups?.length ? h.host.groups : [null]
    for (const g of gs) {
      const id = `group:${h.host.project}:${g?.name ?? ''}`
      const path = g ? [...(g.path ?? []), g.name].join(' / ') : 'ungrouped'
      const f = frames.get(id) ?? {
        label: (manyProjects ? h.host.project + ' · ' : '') + path,
        sort: (g ? path : UNGROUPED) + '\0' + h.host.project,
        members: [],
      }
      f.members.push(h)
      frames.set(id, f)
    }
  }
  const nodes: GNode[] = []
  const containers: GContainer[] = []
  const hostOf = new Map<string, string>()
  for (const [id, f] of [...frames].sort((a, b) => a[1].sort.localeCompare(b[1].sort))) {
    const children: string[] = []
    for (const m of f.members) {
      const nid = id + '|' + m.key
      children.push(nid)
      hostOf.set(nid, m.key)
      nodes.push({
        id: nid,
        title: m.host.address + (m.linked ? ' · ' + m.linked.instance : ' · not placed'),
        label: m.host.name,
        star: (m.host.groups?.length ?? 0) > 1,
        rows: serviceRows(m.host),
      })
    }
    containers.push({ id, label: `${f.label} · ${f.members.length} host${f.members.length === 1 ? '' : 's'}`, children, group: true })
  }
  return { hosts, groups: [...frames.keys()].filter((k) => !k.endsWith(':')).length, nodes, containers, hostOf }
}
