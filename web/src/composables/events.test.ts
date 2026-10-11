import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

/** A controllable stand-in for the browser's EventSource. */
class FakeSource {
  static instances: FakeSource[] = []
  listeners = new Map<string, Array<(e: MessageEvent) => void>>()
  onerror: (() => void) | null = null
  closed = false
  constructor(public url: string) {
    FakeSource.instances.push(this)
  }
  addEventListener(type: string, h: (e: MessageEvent) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), h])
  }
  close() {
    this.closed = true
  }
  fire(type: string, data?: unknown) {
    for (const h of this.listeners.get(type) ?? []) h({ data: data === undefined ? '' : JSON.stringify(data) } as MessageEvent)
  }
  fail() {
    this.onerror?.()
  }
}

describe('events', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    FakeSource.instances = []
    vi.stubGlobal('EventSource', FakeSource)
    vi.resetModules()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('tracks the connection and reconnects with doubling backoff capped at 10s', async () => {
    const ev = await import('./events')
    ev.startEvents()
    expect(ev.connection.value).toBe('connecting')
    expect(FakeSource.instances).toHaveLength(1)
    FakeSource.instances[0].fire('hello')
    expect(ev.connection.value).toBe('open')

    const reconnected = vi.fn()
    ev.onEvent('reconnected', reconnected)
    // drop: 500ms, then 1s, 2s, 4s, 8s, 10s, 10s …
    const delays: number[] = []
    for (const expected of [500, 1000, 2000, 4000, 8000, 10_000, 10_000]) {
      const n = FakeSource.instances.length
      FakeSource.instances[n - 1].fail()
      expect(FakeSource.instances[n - 1].closed).toBe(true)
      expect(ev.connection.value).toBe('reconnecting')
      vi.advanceTimersByTime(expected - 1)
      expect(FakeSource.instances).toHaveLength(n)
      vi.advanceTimersByTime(1)
      expect(FakeSource.instances).toHaveLength(n + 1)
      delays.push(expected)
    }
    expect(reconnected).not.toHaveBeenCalled()
    FakeSource.instances.at(-1)!.fire('hello')
    expect(ev.connection.value).toBe('open')
    expect(reconnected).toHaveBeenCalledTimes(1)
    // after a successful hello the backoff starts again from 500ms
    FakeSource.instances.at(-1)!.fail()
    const n = FakeSource.instances.length
    vi.advanceTimersByTime(500)
    expect(FakeSource.instances).toHaveLength(n + 1)
    expect(delays).toEqual([500, 1000, 2000, 4000, 8000, 10_000, 10_000])
  })

  it('delivers events to subscribers and honours unsubscribe', async () => {
    const ev = await import('./events')
    ev.startEvents()
    const es = FakeSource.instances[0]
    const a = vi.fn()
    const b = vi.fn()
    const offA = ev.onEvent('run.updated', a)
    ev.onEvent('run.updated', b)
    es.fire('run.updated', { id: 1 })
    expect(a).toHaveBeenCalledWith({ id: 1 })
    expect(b).toHaveBeenCalledWith({ id: 1 })
    offA()
    es.fire('run.updated', { id: 2 })
    expect(a).toHaveBeenCalledTimes(1)
    expect(b).toHaveBeenCalledTimes(2)
    // a throwing handler does not stop the others
    ev.onEvent('run.updated', () => {
      throw new Error('bug')
    })
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    es.fire('run.updated', { id: 3 })
    expect(b).toHaveBeenCalledTimes(3)
    expect(spy).toHaveBeenCalled()
    // an empty payload arrives as null
    es.fire('file.changed')
    const c = vi.fn()
    ev.onEvent('file.changed', c)
    es.fire('file.changed')
    expect(c).toHaveBeenCalledWith(null)
    // startEvents is idempotent
    ev.startEvents()
    expect(FakeSource.instances).toHaveLength(1)
  })
})
