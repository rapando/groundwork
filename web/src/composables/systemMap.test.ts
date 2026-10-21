import { describe, expect, it } from 'vitest'
import type { PortRow, SystemModel } from '../api/types'
import { buildSystemMap, hostMeta, serviceRows } from './systemMap'

const port = (p: number, services: string[], flag?: PortRow['flag'], extra: Partial<PortRow> = {}): PortRow => ({
  port: p,
  proto: 'tcp',
  declared: [],
  allowed: [],
  listening: [],
  services,
  flag,
  ...extra,
})
const base = { project: 'ops', env: 'prod', roles: [] as { name: string; playbooks: string[] }[] }

const sys = {
  nodes: [],
  edges: [],
  containers: [],
  rules: {},
  servers: {
    'aws_instance.web': [
      {
        ...base,
        name: 'web-1',
        address: '10.0.0.1',
        groups: [{ name: 'web', path: ['prod'] }, { name: 'monitoring' }],
        roles: [{ name: 'nginx', playbooks: ['site.yml'] }],
        instance: 'aws_instance.web[0]',
        matched_by: 'ip 10.0.0.1',
        firewalls: [],
        ports: [port(443, ['nginx']), port(8080, ['app'], 'blocked'), port(9000, [], 'unused')],
      },
    ],
  },
  unlinked: [{ ...base, name: 'bastion', address: '192.0.2.5', groups: [], reason: 'no instance in state has address 192.0.2.5', ports: [port(22, ['ssh'])] }],
} as unknown as SystemModel

describe('buildSystemMap', () => {
  const m = buildSystemMap(sys)

  it('draws a host once per group it is listed in, starred, and ungrouped hosts last', () => {
    expect(m.containers.map((c) => c.label)).toEqual(['monitoring · 1 host', 'prod / web · 1 host', 'ungrouped · 1 host'])
    const web1 = m.nodes.filter((n) => n.label === 'web-1')
    expect(web1).toHaveLength(2)
    expect(web1.every((n) => n.star)).toBe(true)
    expect(new Set(web1.map((n) => m.hostOf.get(n.id)))).toEqual(new Set(['ops/web-1']))
    expect(m.groups).toBe(2)
  })

  it('names the server a host was placed on, or says it was not', () => {
    expect(m.nodes.find((n) => n.label === 'web-1')!.title).toBe('10.0.0.1 · aws_instance.web[0]')
    const b = m.nodes.find((n) => n.label === 'bastion')!
    expect(b.title).toBe('192.0.2.5 · not placed')
    expect(b.star).toBe(false)
  })
})

describe('serviceRows', () => {
  it('draws a card per service with where it came from, flags bad ports and keeps unused open ports apart', () => {
    const rows = serviceRows(sys.servers['aws_instance.web'][0])
    expect(rows.map((r) => r.text)).toEqual(['nginx', 'app', 'open, nothing uses'])
    expect(rows[0]).toMatchObject({ chips: [{ text: '443' }], card: { dot: 'idle', sub: 'role nginx · site.yml' } })
    expect(rows[0].chips![0].tone).toBeUndefined()
    expect(rows[1]).toMatchObject({ chips: [{ text: '8080', tone: 'warn' }], card: { dot: 'warn', sub: 'no firewall rule for 8080', subTone: 'warn' } })
    expect(rows[2]).toMatchObject({ tone: 'muted', chips: [{ text: '9000', tone: 'muted' }] })
    expect(rows[2].card).toBeUndefined()
  })

  it('after a ports check: green when listening, dashed for what no role declares, roles without ports on one line', () => {
    const h = {
      ...base,
      name: 'db',
      address: 'x',
      groups: [],
      os: 'debian 12',
      listen_at: '2026-10-21T10:00:00Z',
      roles: [
        { name: 'postgres', playbooks: ['db.yml'] },
        { name: 'base', playbooks: ['site.yml'] },
        { name: 'users', playbooks: ['site.yml'] },
      ],
      ports: [
        port(22, ['ssh'], undefined, { declared: ['ansible_port'], listening: ['sshd'] }),
        port(5432, ['postgres'], undefined, { declared: ['postgres_port (role postgres)'], listening: ['postgres'], allowed: ['sg'] }),
        port(6379, ['redis-server'], 'undeclared', { listening: ['redis-server'] }),
        port(8080, ['app'], 'not-listening', { declared: ['app_port'] }),
      ],
    }
    const rows = serviceRows(h)
    expect(rows.map((r) => r.text)).toEqual(['postgres', 'redis-server', 'app', 'also: base, users'])
    expect(rows[0]).toMatchObject({ chips: [{ text: '5432', tone: 'ok' }], card: { dot: 'ok', sub: 'role postgres · db.yml' } })
    expect(rows[1].card).toMatchObject({ dashed: true, sub: 'found listening · no role declares it' })
    expect(rows[2].card).toMatchObject({ dot: 'warn', sub: 'nothing listens on 8080' })
    expect(rows[3]).toEqual({ text: 'also: base, users', tone: 'muted' })
    expect(hostMeta(h)).toBe('debian 12 · ssh 22')
  })

  it('keeps ssh as a card when it has a problem', () => {
    const h = { ...base, name: 'x', address: 'x', groups: [], ports: [port(22, ['ssh'], 'blocked')] }
    expect(serviceRows(h).map((r) => r.text)).toEqual(['ssh'])
    expect(hostMeta(h)).toBe('')
  })

  it('says so when a host has nothing on it', () => {
    expect(serviceRows({ ...base, name: 'x', address: 'x', groups: [], ports: [] })).toEqual([{ text: 'no roles target this host', tone: 'muted' }])
  })
})

describe('summary counts', () => {
  it('counts services and port issues once per host', () => {
    const m = buildSystemMap(sys)
    expect(m.services).toBe(2) // nginx and app on web-1; bastion's ssh goes in its header
    expect(m.issues).toBe(1)
    expect(m.listenAt).toBeUndefined()
    expect(m.nodes.find((n) => n.label === 'bastion')!.meta).toBe('ssh 22')
  })
})
