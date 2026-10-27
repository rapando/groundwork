import { describe, expect, it } from 'vitest'
import { shortPath } from './shortPath'

describe('shortPath', () => {
  it('turns the home folder into ~', () => {
    expect(shortPath('/Users/sam/code/infra')).toBe('~/code/infra')
    expect(shortPath('/home/ops/infra')).toBe('~/infra')
    expect(shortPath('/Users/sam')).toBe('~')
  })
  it('hides the macOS temp folder prefix', () => {
    expect(shortPath('/private/var/folders/s6/hqkmp87s6nq_7978zmvtw1500000gn/T/gw-e2e-01KThv')).toBe('…/T/gw-e2e-01KThv')
    expect(shortPath('/var/folders/s6/abc/T/x')).toBe('…/T/x')
  })
  it('keeps the last segments of a path that is still too long', () => {
    expect(shortPath('/srv/very/deep/tree/of/folders/with/infra/live', 25)).toBe('…/folders/with/infra/live')
    expect(shortPath('/srv/a-really-long-single-folder-name-that-will-not-fit', 20)).toBe('…/a-really-long-single-folder-name-that-will-not-fit')
  })
  it('leaves short paths alone', () => {
    expect(shortPath('/srv/infra')).toBe('/srv/infra')
  })
})
