import { describe, expect, it } from 'vitest'
import { creationOptions, fromB64url, requestOptions, toB64url } from './auth'

describe('passkey option encoding', () => {
  it('round-trips base64url, including padding-free lengths', () => {
    for (const n of [0, 1, 2, 3, 31, 32, 64]) {
      const bytes = Uint8Array.from({ length: n }, (_, i) => (i * 37 + 250) % 256)
      const s = toB64url(bytes.buffer)
      expect(s).not.toMatch(/[+/=]/)
      expect(new Uint8Array(fromB64url(s))).toEqual(bytes)
    }
  })

  it('turns the service JSON into WebAuthn options', () => {
    const c = creationOptions({
      challenge: 'AQID',
      rp: { id: 'localhost', name: 'groundwork' },
      user: { id: 'BAUG', name: 'sam', displayName: 'sam' },
      pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
      excludeCredentials: [{ type: 'public-key', id: 'BwgJ' }],
    })
    expect(new Uint8Array(c.challenge as ArrayBuffer)).toEqual(new Uint8Array([1, 2, 3]))
    expect(new Uint8Array(c.user.id as ArrayBuffer)).toEqual(new Uint8Array([4, 5, 6]))
    expect(new Uint8Array(c.excludeCredentials![0].id as ArrayBuffer)).toEqual(new Uint8Array([7, 8, 9]))
    expect(c.rp.id).toBe('localhost')

    const r = requestOptions({ challenge: 'AQID', userVerification: 'required' })
    expect(r.allowCredentials).toEqual([])
    expect(r.userVerification).toBe('required')
  })
})
