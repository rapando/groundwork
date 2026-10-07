export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string, public details?: unknown) {
    super(message)
  }
}

function token(): string {
  return document.querySelector<HTMLMetaElement>('meta[name="groundwork-token"]')?.content ?? ''
}

async function parseError(res: Response): Promise<ApiError> {
  try {
    const body = await res.json()
    const e = body.error ?? {}
    return new ApiError(res.status, e.code ?? 'error', e.message ?? res.statusText, e.details)
  } catch {
    return new ApiError(res.status, 'error', res.statusText)
  }
}

export async function api<T>(path: string, init: { method?: string; body?: unknown; headers?: Record<string, string> } = {}): Promise<T> {
  const res = await fetch('/api' + path, {
    method: init.method ?? (init.body === undefined ? 'GET' : 'POST'),
    headers: {
      'X-Groundwork-Token': token(),
      ...(init.body === undefined ? {} : { 'Content-Type': 'application/json' }),
      ...(init.headers ?? {}),
    },
    body: init.body === undefined ? undefined : JSON.stringify(init.body),
  })
  if (!res.ok) throw await parseError(res)
  return (await res.json()) as T
}

export async function apiBlob(path: string, body: unknown): Promise<Blob> {
  const res = await fetch('/api' + path, {
    method: 'POST',
    headers: { 'X-Groundwork-Token': token(), 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw await parseError(res)
  return res.blob()
}

/** Field errors from a 422 invalid_form response, keyed by field name. */
export function fieldErrors(e: unknown): Record<string, string[]> {
  const out: Record<string, string[]> = {}
  if (e instanceof ApiError && Array.isArray(e.details)) {
    for (const d of e.details as { field: string; message: string }[]) {
      ;(out[d.field] ??= []).push(d.message)
    }
  }
  return out
}
