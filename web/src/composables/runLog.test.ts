import { defineComponent, h, nextTick, ref } from 'vue'
import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { LogLine, LogPage } from '../api/types'

const handlers: Record<string, (d: any) => void> = {}
vi.mock('./events', () => ({
  onEvent: (type: string, h: (d: any) => void) => {
    handlers[type] = h
    return () => {
      delete handlers[type]
    }
  },
}))
const api = vi.fn()
vi.mock('../api/client', () => ({ api: (...a: unknown[]) => api(...a) }))

import { useRunLog } from './runLog'

const line = (n: number, level: LogLine['level'] = 'info', text = `line ${n}`): LogLine => ({ n, t: '', stage: 's', level, text })

/** Serves a log of `total` lines, page by page, as the server would. */
function serve(total: number) {
  const calls: string[] = []
  api.mockImplementation(async (path: string): Promise<LogPage> => {
    calls.push(path)
    const u = new URL('http://x' + path)
    const from = Number(u.searchParams.get('from'))
    const limit = Number(u.searchParams.get('limit'))
    const lines: LogLine[] = []
    for (let n = from; n < Math.min(from + limit, total); n++) lines.push(line(n))
    return { lines, next: from + lines.length, total }
  })
  return calls
}

function mountLog(runId = 1) {
  const id = ref<number | null>(runId)
  const level = ref('debug')
  const query = ref('')
  let log!: ReturnType<typeof useRunLog>
  const onRes = vi.fn()
  const w = mount(
    defineComponent({
      setup() {
        log = useRunLog(id, level, query, onRes)
        return () => h('div')
      },
    }),
  )
  return { id, level, query, log, onRes, w }
}

const flush = async () => {
  for (let i = 0; i < 20; i++) await nextTick()
  await new Promise((r) => setTimeout(r, 0))
  for (let i = 0; i < 20; i++) await nextTick()
}

describe('useRunLog', () => {
  beforeEach(() => {
    api.mockReset()
    for (const k of Object.keys(handlers)) delete handlers[k]
  })

  it('loads a huge log from the tail in pages and reports what it skipped', async () => {
    const calls = serve(50_000)
    const { log } = mountLog()
    await flush()
    expect(log.total.value).toBe(50_000)
    expect(log.skipped.value).toBe(30_000) // INITIAL_MAX = 20000 lines kept
    expect(log.lines.value).toHaveLength(20_000)
    expect(log.lines.value[0].n).toBe(30_000)
    expect(log.lines.value.at(-1)!.n).toBe(49_999)
    // one probe, then 2000-line pages until a short page
    const pages = calls.filter((c) => !c.includes('limit=1&'))
    expect(pages).toHaveLength(11) // 10 full pages + the empty one that ends the loop
    expect(pages[0]).toContain('from=30000&limit=2000')
    expect(log.loading.value).toBe(false)
  })

  it('appends live lines once, in order, filtered by level, and buffers while loading', async () => {
    serve(3)
    const { log, level, onRes } = mountLog()
    await flush()
    expect(log.lines.value.map((l) => l.n)).toEqual([0, 1, 2])
    // a duplicate of a loaded line is ignored; new ones are appended
    handlers['log.line']({ run: 1, lines: [line(2), line(3), line(4, 'debug'), line(5, 'error', 'Error: x')] })
    expect(log.lines.value.map((l) => l.n)).toEqual([0, 1, 2, 3, 4, 5])
    expect(log.total.value).toBe(6)
    // another run's lines are not ours
    handlers['log.line']({ run: 2, lines: [line(99)] })
    expect(log.lines.value).toHaveLength(6)
    // resource events are reported even when the line is filtered out
    handlers['log.line']({ run: 1, lines: [{ ...line(6, 'debug'), res: { address: 'a.b', action: 'create', status: 'done' } }] })
    expect(onRes).toHaveBeenCalledTimes(1)
    // raising the level reloads with the server filter and the client filter agrees
    level.value = 'warn'
    await new Promise((r) => setTimeout(r, 300))
    await flush()
    handlers['log.line']({ run: 1, lines: [line(7, 'info'), line(8, 'warn')] })
    expect(log.lines.value.some((l) => l.n === 7)).toBe(false)
    expect(log.lines.value.some((l) => l.n === 8)).toBe(true)
  })

  it('unsubscribes when the component unmounts', async () => {
    serve(1)
    const { w } = mountLog()
    await flush()
    expect(Object.keys(handlers).sort()).toEqual(['log.line', 'reconnected'])
    w.unmount()
    expect(Object.keys(handlers)).toEqual([])
  })
})
