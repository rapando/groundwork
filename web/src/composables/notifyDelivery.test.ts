import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Notification } from '../api/types'
import { clearTab, dismissToast, FADE_MS, flagTab, holdToast, pushToast, release, toasts } from './notifyDelivery'

vi.mock('../api/client', () => ({ api: vi.fn(), serviceApi: vi.fn() }))
vi.mock('../project', () => ({ projectId: 'infra' }))

const note = (o: Partial<Notification>): Notification => ({
  id: 1,
  event: 'failed',
  run_id: 14,
  kind: 'ans.playbook',
  stage: '',
  label: 'ansible-playbook site.yml',
  root: '',
  env: 'prod',
  title: 'Playbook site.yml failed on prod',
  text: '',
  duration_s: 1,
  at: '2026-10-21T14:30:00Z',
  toast: true,
  desktop: true,
  ...o,
})

describe('toasts', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => {
    for (const t of [...toasts.value]) dismissToast(t.n.id)
    vi.useRealTimers()
  })

  it('keeps failures until dismissed and fades successes after 6s', () => {
    pushToast(note({ id: 1 }))
    pushToast(note({ id: 2, event: 'succeeded' }))
    expect(toasts.value.map((t) => t.n.id)).toEqual([2, 1])
    vi.advanceTimersByTime(FADE_MS)
    expect(toasts.value.map((t) => t.n.id)).toEqual([1])
    dismissToast(1)
    expect(toasts.value).toEqual([])
  })

  it('holds a fading toast while you hover it', () => {
    pushToast(note({ id: 2, event: 'succeeded' }))
    holdToast(2)
    vi.advanceTimersByTime(FADE_MS * 2)
    expect(toasts.value).toHaveLength(1)
    release(2)
    vi.advanceTimersByTime(FADE_MS)
    expect(toasts.value).toHaveLength(0)
  })

  it('shows each notification once and drops fading ones first when full', () => {
    pushToast(note({ id: 1 }))
    pushToast(note({ id: 1 }))
    pushToast(note({ id: 2, event: 'succeeded' }))
    pushToast(note({ id: 3 }))
    pushToast(note({ id: 4 }))
    pushToast(note({ id: 5 }))
    expect(toasts.value.map((t) => t.n.id)).toEqual([5, 4, 3, 1])
  })
})

describe('tab title', () => {
  let hidden = true
  beforeEach(() => {
    hidden = true
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
    document.title = 'groundwork'
  })

  it('flags a hidden tab, keeps the worst news, and clears when you look', () => {
    flagTab(note({ id: 1 }))
    expect(document.title).toBe('✕ Failed · site.yml · groundwork')
    flagTab(note({ id: 2, event: 'succeeded' }))
    expect(document.title).toBe('✕ Failed · site.yml · groundwork')
    clearTab()
    expect(document.title).toBe('groundwork')
  })

  it('leaves a visible tab alone', () => {
    hidden = false
    flagTab(note({ id: 1 }))
    expect(document.title).toBe('groundwork')
  })
})
