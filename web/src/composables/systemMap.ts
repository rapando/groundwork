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
  services: number // service cards, counted once per host
  issues: number // ports blocked or not listening, once per host
  listenAt?: string // the newest listening-ports check
}

export const portLabel = (p: { port: number; proto: string }) => (p.proto === 'tcp' ? String(p.port) : `${p.port}/${p.proto}`)
export const isFlagged = (p: PortRow) => p.flag === 'blocked' || p.flag === 'not-listening'
/** What is wrong with a flagged port, in a few words. */
export const portProblem = (p: PortRow) =>
  p.flag === 'blocked' ? `no firewall rule for ${portLabel(p)}` : p.flag === 'not-listening' ? `nothing listens on ${portLabel(p)}` : ''

// a port chip: amber when blocked or not listening, green when the last check found it listening and nothing blocks it
function chip(p: PortRow, checked: boolean, muted = false): GChip {
  if (isFlagged(p)) return { text: portLabel(p), tone: 'warn' }
  if (muted) return { text: portLabel(p), tone: 'muted' }
  return { text: portLabel(p), tone: checked && p.listening.length ? 'ok' : undefined }
}
// ok: every port declared, allowed and listening; warn: one is blocked or not listening; idle: not checked yet
function dot(ports: PortRow[], checked: boolean): 'ok' | 'warn' | 'idle' {
  if (ports.some(isFlagged)) return 'warn'
  return checked ? 'ok' : 'idle'
}
const where = (playbooks: string[]) => (playbooks.length > 2 ? `${playbooks[0]} +${playbooks.length - 1}` : playbooks.join(', '))
const SSH = 'ssh'

/**
 * The host's services as cards: each role with ports (status dot, port chips,
 * "role nginx · site.yml" or what is wrong), then services found listening that
 * no role declares (dashed), then open ports nothing uses, then the roles with
 * no ports on one "also:" line. ssh goes in the host header unless it has a problem.
 */
export function serviceRows(h: MapHost['host']): GNodeRow[] {
  const checked = !!h.listen_at
  const roles = new Map(h.roles.map((r) => [r.name, r.playbooks]))
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
  const rows: GNodeRow[] = []
  const also: string[] = []
  for (const [s, ports] of byService) {
    if (!ports.length) {
      also.push(s)
      continue
    }
    if (s === SSH && !roles.has(s) && !ports.some(isFlagged)) continue // drawn in the header
    const problem = ports.map(portProblem).find(Boolean)
    const pb = roles.get(s)
    const found = !pb && ports.every((p) => !p.declared.length && p.listening.length)
    const source = pb
      ? `role ${s}${pb.length ? ' · ' + where(pb) : ''}`
      : found
        ? 'found listening · no role declares it'
        : `declared by ${ports.flatMap((p) => p.declared)[0] ?? 'a variable'}`
    rows.push({
      text: s,
      chips: ports.map((p) => chip(p, checked)),
      card: {
        dot: dot(ports, checked),
        dashed: found || undefined,
        sub: problem ? (pb ? `role ${s} · ${problem}` : problem) : source,
        subTone: problem ? 'warn' : undefined,
      },
    })
  }
  if (loose.length) rows.push({ text: 'open, nothing uses', tone: 'muted', chips: loose.map((p) => chip(p, checked, true)) })
  if (also.length) rows.push({ text: 'also: ' + also.join(', '), tone: 'muted' })
  if (!rows.length) rows.push({ text: h.roles.length ? 'no ports found' : 'no roles target this host', tone: 'muted' })
  return rows
}

/** The host header note: OS when known, and the ssh port when it has no problem. */
export function hostMeta(h: MapHost['host']): string {
  const ssh = h.ports.find((p) => p.services?.includes(SSH) && !isFlagged(p) && !h.roles.some((r) => r.name === SSH))
  return [h.os, ssh ? `${SSH} ${portLabel(ssh)}` : ''].filter(Boolean).join(' · ')
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
        meta: hostMeta(m.host) || undefined,
        rows: serviceRows(m.host),
      })
    }
    containers.push({ id, label: `${f.label} · ${f.members.length} host${f.members.length === 1 ? '' : 's'}`, children, group: true })
  }
  let services = 0,
    issues = 0
  let listenAt: string | undefined
  for (const h of hosts) {
    services += serviceRows(h.host).filter((r) => r.card).length
    issues += h.host.ports.filter(isFlagged).length
    if (h.host.listen_at && (!listenAt || Date.parse(h.host.listen_at) > Date.parse(listenAt))) listenAt = h.host.listen_at
  }
  return { hosts, groups: [...frames.keys()].filter((k) => !k.endsWith(':')).length, nodes, containers, hostOf, services, issues, listenAt }
}
