// Browser sign-in: a password and/or passkeys (WebAuthn: Touch ID, Windows
// Hello, a security key). The /auth endpoints work signed out; the account
// ones under /api/auth need a signed-in page (it carries the API token).

/** Mirrors auth.MinPassword in the service. */
export const MIN_PASSWORD = 8

export interface AuthStatus {
  configured: boolean
  password: boolean
  passkeys: boolean
  passkeys_available: boolean
  signed_in: boolean
  setup_allowed: boolean
}

export interface PasskeyInfo {
  id: string
  name: string
  created: string
  last_used?: string
}

export interface Account {
  password: boolean
  passkeys: PasskeyInfo[]
  passkeys_available: boolean
}

export class AuthError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
  }
}

type CallInit = { method?: string; body?: unknown; headers?: Record<string, string>; raw?: string }

async function call<T>(path: string, init: CallInit = {}): Promise<T> {
  const method = init.method ?? 'POST'
  const res = await fetch('/auth' + path, {
    method,
    headers: method === 'GET' ? {} : { 'Content-Type': 'application/json', ...(init.headers ?? {}) },
    body: init.raw ?? (init.body === undefined ? undefined : JSON.stringify(init.body)),
  })
  if (!res.ok) {
    let code = 'error'
    let message = res.statusText
    try {
      const e = (await res.json()).error ?? {}
      code = e.code ?? code
      message = e.message ?? message
    } catch {
      /* not JSON */
    }
    throw new AuthError(res.status, code, message)
  }
  return (await res.json()) as T
}

export const authStatus = () => call<AuthStatus>('/status', { method: 'GET' })
export const setupPassword = (password: string) => call('/password/setup', { body: { password } })
export const loginPassword = (password: string) => call('/password/login', { body: { password } })
export const signOut = () => call('/sign-out', { body: {} })

/** True when this browser can use passkeys at all. */
export function passkeysSupported(): boolean {
  return typeof window !== 'undefined' && !!window.PublicKeyCredential && !!navigator.credentials
}

/** Adds a passkey: first-run setup when signed out, otherwise another one for this account. */
export async function registerPasskey(name = ''): Promise<void> {
  const begin = await call<{ ceremony: string; options: { publicKey: Record<string, unknown> } }>('/passkeys/register/begin', { body: {} })
  const cred = (await navigator.credentials.create({ publicKey: creationOptions(begin.options.publicKey) })) as PublicKeyCredential | null
  if (!cred) throw new AuthError(0, 'cancelled', 'No passkey was created.')
  const label = name.trim() || defaultName(cred)
  await call('/passkeys/register/finish?name=' + encodeURIComponent(label), {
    headers: { 'X-Groundwork-Ceremony': begin.ceremony },
    raw: JSON.stringify(credentialJSON(cred)),
  })
}

/** Signs in with a stored passkey. */
export async function loginPasskey(): Promise<void> {
  const begin = await call<{ ceremony: string; options: { publicKey: Record<string, unknown> } }>('/passkeys/login/begin', { body: {} })
  const cred = (await navigator.credentials.get({ publicKey: requestOptions(begin.options.publicKey) })) as PublicKeyCredential | null
  if (!cred) throw new AuthError(0, 'cancelled', 'No passkey was used.')
  await call('/passkeys/login/finish', { headers: { 'X-Groundwork-Ceremony': begin.ceremony }, raw: JSON.stringify(credentialJSON(cred)) })
}

/** A browser that dismissed the prompt reports NotAllowedError: not worth an alarming message. */
export function describe(e: unknown): string {
  if (e instanceof DOMException && e.name === 'NotAllowedError') return 'The passkey prompt was closed or timed out.'
  if (e instanceof DOMException && e.name === 'InvalidStateError') return 'This authenticator already holds a passkey for groundwork.'
  return e instanceof Error ? e.message : String(e)
}

// --- base64url <-> bytes, and the option and credential shapes ---

export function fromB64url(s: string): ArrayBuffer {
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/') + '==='.slice((s.length + 3) % 4)
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out.buffer
}

export function toB64url(buf: ArrayBuffer | null): string {
  if (!buf) return ''
  const bytes = new Uint8Array(buf)
  let bin = ''
  for (const b of bytes) bin += String.fromCharCode(b)
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

type Descriptor = { type: PublicKeyCredentialType; id: string; transports?: AuthenticatorTransport[] }
const descriptors = (list: unknown) => ((list as Descriptor[] | undefined) ?? []).map((d) => ({ ...d, id: fromB64url(d.id) }))

export function creationOptions(o: Record<string, unknown>): PublicKeyCredentialCreationOptions {
  const user = o.user as { id: string; name: string; displayName: string }
  return {
    ...(o as unknown as PublicKeyCredentialCreationOptions),
    challenge: fromB64url(o.challenge as string),
    user: { ...user, id: fromB64url(user.id) },
    excludeCredentials: descriptors(o.excludeCredentials),
  }
}

export function requestOptions(o: Record<string, unknown>): PublicKeyCredentialRequestOptions {
  return {
    ...(o as unknown as PublicKeyCredentialRequestOptions),
    challenge: fromB64url(o.challenge as string),
    allowCredentials: descriptors(o.allowCredentials),
  }
}

function credentialJSON(c: PublicKeyCredential): Record<string, unknown> {
  const r = c.response
  const response: Record<string, unknown> = { clientDataJSON: toB64url(r.clientDataJSON) }
  if ('attestationObject' in r) {
    const a = r as AuthenticatorAttestationResponse
    response.attestationObject = toB64url(a.attestationObject)
    response.transports = a.getTransports?.() ?? []
  } else {
    const a = r as AuthenticatorAssertionResponse
    response.authenticatorData = toB64url(a.authenticatorData)
    response.signature = toB64url(a.signature)
    response.userHandle = toB64url(a.userHandle)
  }
  return {
    id: c.id,
    rawId: toB64url(c.rawId),
    type: c.type,
    authenticatorAttachment: c.authenticatorAttachment ?? undefined,
    clientExtensionResults: c.getClientExtensionResults(),
    response,
  }
}

function defaultName(c: PublicKeyCredential): string {
  if (c.authenticatorAttachment !== 'platform') return 'Security key or phone'
  const ua = navigator.userAgent
  if (/Mac/.test(ua)) return 'This Mac (Touch ID)'
  if (/Windows/.test(ua)) return 'This PC (Windows Hello)'
  return 'This device'
}
