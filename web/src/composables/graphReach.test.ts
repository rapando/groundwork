import { describe, expect, it } from 'vitest'
import type { GraphNode } from '../api/types'
import { asFilter, fadeMap, filterCounts, findNodes, FADE, reach, riskyDownstream } from './graphReach'

const node = (id: string, extra: Partial<GraphNode> = {}): GraphNode => {
  const [type, name] = id.split('.')
  return { id, kind: 'resource', type, name, file: 'main.tf', line: 1, end_line: 2, ...extra }
}
// vpc → subnet → instance → lb → dns ; vpc → sg → instance ; unrelated file
const nodes = [
  node('aws_vpc.main'),
  node('aws_subnet.private', { file: 'network.tf' }),
  node('aws_security_group.app'),
  node('aws_instance.app', { action: 'update' }),
  node('aws_lb.app', { action: 'replace' }),
  node('aws_route53_record.app'),
  node('local_file.motd', { drift: true, errors: 2 }),
]
const edges = [
  { from: 'aws_vpc.main', to: 'aws_subnet.private' },
  { from: 'aws_vpc.main', to: 'aws_security_group.app' },
  { from: 'aws_subnet.private', to: 'aws_instance.app' },
  { from: 'aws_security_group.app', to: 'aws_instance.app' },
  { from: 'aws_instance.app', to: 'aws_lb.app' },
  { from: 'aws_lb.app', to: 'aws_route53_record.app' },
]

describe('reach', () => {
  const r = reach(edges, 'aws_subnet.private')

  it('follows uses and used-by all the way, not only direct neighbours', () => {
    expect([...r.up]).toEqual(['aws_vpc.main'])
    expect([...r.down].sort()).toEqual(['aws_instance.app', 'aws_lb.app', 'aws_route53_record.app'])
    expect([...r.directDown]).toEqual(['aws_instance.app'])
    expect([...r.directUp]).toEqual(['aws_vpc.main'])
  })

  it('lights only the edges on a path through the selection', () => {
    expect([...r.edges].sort()).toEqual([
      'aws_instance.app>aws_lb.app',
      'aws_lb.app>aws_route53_record.app',
      'aws_subnet.private>aws_instance.app',
      'aws_vpc.main>aws_subnet.private',
    ])
  })

  it('stops on cycles', () => {
    const c = reach(
      [
        { from: 'a', to: 'b' },
        { from: 'b', to: 'a' },
      ],
      'a',
    )
    expect([...c.up]).toEqual(['b'])
    expect([...c.down]).toEqual(['b'])
  })

  it('warns about what the plan replaces or destroys downstream', () => {
    expect(riskyDownstream(nodes, r.down).map((n) => n.id)).toEqual(['aws_lb.app'])
    expect(riskyDownstream(nodes, reach(edges, 'aws_lb.app').down)).toEqual([])
  })
})

describe('filters and search', () => {
  it('counts each filter', () => {
    expect(filterCounts(nodes)).toEqual({ all: 7, plan: 2, drifted: 1, errors: 1 })
  })

  it('reads the filter from the URL, defaulting to all', () => {
    expect(asFilter('drifted')).toBe('drifted')
    expect(asFilter(undefined)).toBe('all')
    expect(asFilter('nonsense')).toBe('all')
  })

  it('finds by address, type or file, prefix matches first', () => {
    expect(findNodes(nodes, 'all', 'network.tf').map((n) => n.id)).toEqual(['aws_subnet.private'])
    expect(findNodes(nodes, 'all', 'app').map((n) => n.id)[0]).toBe('aws_security_group.app')
    expect(findNodes(nodes, 'all', 'aws_lb').map((n) => n.id)).toEqual(['aws_lb.app'])
    expect(findNodes(nodes, 'plan', 'aws').map((n) => n.id)).toEqual(['aws_instance.app', 'aws_lb.app'])
  })

  it('fades the rest, neighbours of matches less so', () => {
    const ids = nodes.map((n) => n.id)
    const f = fadeMap(ids, new Set(['aws_lb.app']), edges)!
    expect(f.get('aws_lb.app')).toBe(FADE.match)
    expect(f.get('aws_instance.app')).toBe(FADE.near)
    expect(f.get('aws_route53_record.app')).toBe(FADE.near)
    expect(f.get('aws_vpc.main')).toBe(FADE.rest)
    expect(fadeMap(ids, new Set(ids), edges)).toBeNull()
  })
})
