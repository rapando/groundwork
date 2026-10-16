import { describe, expect, it } from 'vitest'
import type { PortRow, SystemModel } from '../api/types'
import { buildSystemMap, serviceRows } from './systemMap'

const port = (p: number, services: string[], flag?: PortRow['flag']): PortRow => ({
  port: p,
  proto: 'tcp',
  declared: [],
  allowed: [],
  listening: [],
  services,
  flag,
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
  it('lists roles first with their ports, flags bad ports and keeps unused open ports apart', () => {
    const rows = serviceRows(sys.servers['aws_instance.web'][0])
    expect(rows.map((r) => r.text)).toEqual(['nginx', 'app', 'open'])
    expect(rows[0].chips).toEqual([{ text: '443', tone: undefined }])
    expect(rows[1].chips).toEqual([{ text: '8080', tone: 'warn' }])
    expect(rows[2]).toMatchObject({ tone: 'muted', chips: [{ text: '9000', tone: 'muted' }] })
  })

  it('says so when a host has nothing on it', () => {
    expect(serviceRows({ ...base, name: 'x', address: 'x', groups: [], ports: [] })).toEqual([{ text: 'no roles or ports found', tone: 'muted' }])
  })
})
