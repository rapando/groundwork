import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Notification } from '../api/types'

const api = vi.fn()
const serviceApi = vi.fn()
const handlers = new Map<string, (d: unknown) => void>()
vi.mock('../api/client', () => ({ api: (...a: unknown[]) => api(...a), serviceApi: (...a: unknown[]) => serviceApi(...a) }))
vi.mock('../project', () => ({ projectId: 'infra' }))
vi.mock('../composables/events', () => ({
  onEvent: (t: string, h: (d: unknown) => void) => {
    handlers.set(t, h)
    return () => handlers.delete(t)
  },
}))

const note = (o: Partial<Notification>): Notification => ({
  id: 1,
  event: 'failed',
  run_id: 14,
  kind: 'tf.plan',
  stage: 'apply',
  label: 'terraform apply',
  root: 'terraform/envs/prod',
  env: 'prod',
  title: 'Apply #14 failed on prod',
  text: '',
  duration_s: 1,
  at: '2026-10-21T14:30:00Z',
  toast: true,
  desktop: true,
  ...o,
})

async function fresh() {
  const m = await import('./notifications')
  m.resetNotifications()
  return m
}

describe('notifications store', () => {
  beforeEach(() => {
    api.mockReset()
    serviceApi.mockReset()
    handlers.clear()
    api.mockImplementation(async (path: string) => {
      if (path.startsWith('/notifications?')) return { items: [note({ id: 2, run_id: 15, event: 'approval' }), note({ id: 1 })], unread: 2 }
      if (path === '/notifications/read') return { unread: 1 }
      throw new Error('unexpected ' + path)
    })
    serviceApi.mockImplementation(async (path: string) => {
      if (path.startsWith('/notifications?')) return { items: [note({ id: 9, project: 'edge', project_name: 'edge-config', event: 'approval' })], unread: 3 }
      return { unread: 0 }
    })
  })
  afterEach(() => vi.useRealTimers())

  it('loads the project and service lists', async () => {
    const m = await fresh()
    const s = m.useNotifications()
    await s.load()
    expect(s.items.value.map((n) => n.id)).toEqual([2, 1])
    expect(s.unread.value).toBe(2)
    expect(s.serviceUnread.value).toBe(3)
    expect([...s.unreadRuns.value].sort()).toEqual([14, 15])
    expect(s.unreadFailed.value).toBe(1)
  })

  it('marks a run read and skips the request when nothing is unread for it', async () => {
    const m = await fresh()
    const s = m.useNotifications()
    await s.load()
    await s.markRunRead(14)
    expect(api).toHaveBeenCalledWith('/notifications/read', { body: { run_id: 14 } })
    expect(s.items.value.find((n) => n.id === 1)?.read_at).toBeTruthy()
    expect(s.unread.value).toBe(1)
    expect(s.unreadRuns.value.has(14)).toBe(false)
    api.mockClear()
    await s.markRunRead(14)
    expect(api).not.toHaveBeenCalled()
  })

  it('marks another project’s notification read through its project API', async () => {
    const m = await fresh()
    const s = m.useNotifications()
    await s.load()
    await s.markRead(s.serviceItems.value[0])
    expect(serviceApi).toHaveBeenCalledWith('/p/edge/notifications/read', { body: { ids: [9] } })
    expect(s.serviceUnread.value).toBe(2)
    expect(s.unread.value).toBe(2) // this project's count is untouched
  })

  it('marks everything read', async () => {
    const m = await fresh()
    const s = m.useNotifications()
    await s.load()
    await s.markServiceAllRead()
    expect(serviceApi).toHaveBeenCalledWith('/notifications/read', { body: { all: true } })
    expect(s.serviceUnread.value).toBe(0)
    expect(s.unread.value).toBe(0)
    expect(s.items.value.every((n) => n.read_at)).toBe(true)
  })

  it('takes live notifications from the stream once and tells listeners', async () => {
    const m = await fresh()
    const s = m.useNotifications()
    m.startNotifications()
    await vi.waitFor(() => expect(s.unread.value).toBe(2))
    const seen: number[] = []
    m.onNotification((n) => seen.push(n.id))
    handlers.get('notification.new')!(note({ id: 3, run_id: 16 }))
    handlers.get('notification.new')!(note({ id: 3, run_id: 16 }))
    expect(seen).toEqual([3])
    expect(s.items.value[0].id).toBe(3)
    expect(s.unread.value).toBe(3)
    expect(s.serviceUnread.value).toBe(4)
    expect(s.serviceItems.value[0]).toMatchObject({ id: 3, project: 'infra' })
    handlers.get('notifications.updated')!({ unread: 0 })
    expect(s.unread.value).toBe(0)
  })

  it('polls the service list for the bell', async () => {
    vi.useFakeTimers()
    const m = await fresh()
    m.startNotifications(1000)
    await vi.advanceTimersByTimeAsync(0)
    const before = serviceApi.mock.calls.length
    await vi.advanceTimersByTimeAsync(3000)
    expect(serviceApi.mock.calls.length - before).toBe(3)
    m.resetNotifications()
  })
})
