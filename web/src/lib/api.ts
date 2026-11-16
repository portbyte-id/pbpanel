/**
 * The panel's HTTP client.
 *
 * Three rules the server enforces, mirrored here so a caller cannot forget them:
 *
 *  - unsafe methods carry `X-CSRF-Token` (the token the login response handed
 *    back and that `GET /api/me` re-issues after a reload);
 *  - a 401 means the session is gone, so the app is sent to `/login` instead of
 *    showing an error the operator cannot act on;
 *  - every failure arrives as the `{error, detail}` envelope and becomes an
 *    `ApiError`, so a page branches on a code and shows the operator's sentence.
 */

/** The server's only error shape: a stable class plus a sentence for the operator. */
export interface ErrorEnvelope {
  error: string
  detail: string
}

/** Header the server compares against the session's CSRF token. */
export const CSRF_HEADER = "X-CSRF-Token"

export interface LoginResponse {
  username: string
  csrf_token: string
}

export interface MeResponse {
  username: string
  csrf_token: string
}

/** One key on the Settings page, with the registry metadata needed to render it. */
export interface SettingKey {
  name: string
  type: "int" | "string" | "bool" | "duration" | "port" | "path" | "enum"
  value: string
  default: string
  is_default: boolean
  restart_required: boolean
  description: string
  group: string
  allowed?: string[]
}

export interface SettingsGroup {
  name: string
  keys: SettingKey[]
}

export interface SettingsResponse {
  groups: SettingsGroup[]
}

export interface SettingWrite {
  name: string
  value: string
}

export interface AuditEntry {
  id: number
  at: string
  actor: string
  action: string
  target: string
  result: "success" | "failure"
  detail: unknown
}

export interface AuditResponse {
  entries: AuditEntry[]
  limit: number
}

/** A failed request. `code` is the envelope's `error`, `detail` its sentence. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly detail: string

  constructor(status: number, code: string, detail: string) {
    super(detail || code)
    this.name = "ApiError"
    this.status = status
    this.code = code
    this.detail = detail
  }

  get isUnauthorized(): boolean {
    return this.status === 401
  }
}

/** Methods the server treats as read-only, and therefore CSRF-exempt. */
export function isSafeMethod(method: string): boolean {
  const m = method.toUpperCase()
  return m === "GET" || m === "HEAD" || m === "OPTIONS"
}

function isErrorEnvelope(value: unknown): value is ErrorEnvelope {
  if (typeof value !== "object" || value === null) return false
  const candidate = value as Record<string, unknown>
  return typeof candidate.error === "string" && typeof candidate.detail === "string"
}

export interface ApiClientOptions {
  /** Override for tests; defaults to the page's `fetch`. */
  fetch?: typeof globalThis.fetch
  /** Prefix for every path, for a client that is not same-origin. Defaults to "". */
  baseUrl?: string
  /** Called when a non-login request comes back 401. Wired to the router at boot. */
  onUnauthorized?: () => void
}

export class ApiClient {
  readonly #fetch: typeof globalThis.fetch
  readonly #baseUrl: string
  #csrfToken: string | null = null
  #onUnauthorized: (() => void) | null

  constructor(options: ApiClientOptions = {}) {
    this.#fetch = options.fetch ?? globalThis.fetch.bind(globalThis)
    this.#baseUrl = options.baseUrl ?? ""
    this.#onUnauthorized = options.onUnauthorized ?? null
  }

  /** Install the handler that sends the app to `/login`. */
  setUnauthorizedHandler(handler: (() => void) | null): void {
    this.#onUnauthorized = handler
  }

  /** The token for this session, or null before login and before `/api/me`. */
  get csrfToken(): string | null {
    return this.#csrfToken
  }

  setCsrfToken(token: string | null): void {
    this.#csrfToken = token
  }

  /** POST /api/login. A 401 here is bad credentials, not a lost session. */
  async login(username: string, password: string): Promise<LoginResponse> {
    const res = await this.request<LoginResponse>("/api/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    })
    this.setCsrfToken(res.csrf_token)
    return res
  }

  /** POST /api/logout. The session is revoked either way, so a failure is not fatal. */
  async logout(): Promise<void> {
    try {
      await this.request<void>("/api/logout", { method: "POST" })
    } finally {
      this.setCsrfToken(null)
    }
  }

  /** GET /api/me. Re-issues the CSRF token the page lost on reload. */
  async me(): Promise<MeResponse> {
    const res = await this.request<MeResponse>("/api/me")
    this.setCsrfToken(res.csrf_token)
    return res
  }

  /** POST /api/password. Every session is revoked, including this one. */
  async changePassword(currentPassword: string, newPassword: string): Promise<void> {
    await this.request<void>("/api/password", {
      method: "POST",
      body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
    })
    this.setCsrfToken(null)
  }

  async settings(): Promise<SettingsResponse> {
    return this.request<SettingsResponse>("/api/settings")
  }

  /** PUT /api/settings. Writes are applied in order; the first refusal stops it. */
  async saveSettings(settings: SettingWrite[]): Promise<SettingsResponse> {
    return this.request<SettingsResponse>("/api/settings", {
      method: "PUT",
      body: JSON.stringify({ settings }),
    })
  }

  async audit(limit?: number): Promise<AuditResponse> {
    const query = limit === undefined ? "" : `?limit=${limit}`
    return this.request<AuditResponse>(`/api/audit${query}`)
  }

  /**
   * One request through the whole contract: headers, envelope, 401 handling.
   * `T` is the success body; a 204 resolves to undefined.
   */
  async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const method = (init.method ?? "GET").toUpperCase()
    const headers = new Headers(init.headers)
    headers.set("Accept", "application/json")
    if (init.body !== undefined && !headers.has("Content-Type")) {
      headers.set("Content-Type", "application/json")
    }
    if (!isSafeMethod(method) && this.#csrfToken !== null) {
      headers.set(CSRF_HEADER, this.#csrfToken)
    }

    const res = await this.#fetch(this.#baseUrl + path, {
      ...init,
      method,
      headers,
      credentials: "same-origin",
    })

    const body = await readBody(res)

    if (!res.ok) {
      const envelope: ErrorEnvelope = isErrorEnvelope(body)
        ? body
        : { error: `http_${res.status}`, detail: res.statusText || "the request failed" }
      const error = new ApiError(res.status, envelope.error, envelope.detail)
      // A 401 on login is a wrong password, not an expired session: redirecting
      // there would replace the form's own message with a pointless reload.
      if (error.isUnauthorized && !isLoginPath(path)) {
        this.#onUnauthorized?.()
      }
      throw error
    }

    if (res.status === 204) {
      return undefined as T
    }
    return body as T
  }
}

async function readBody(res: Response): Promise<unknown> {
  const text = await res.text()
  if (text === "") return undefined
  try {
    return JSON.parse(text) as unknown
  } catch {
    // A non-JSON body (a proxy error page, a truncated stream) is not the
    // envelope; the caller turns it into a generic ApiError.
    return undefined
  }
}

function isLoginPath(path: string): boolean {
  return path.split("?")[0] === "/api/login"
}

/** The client the app uses. Tests construct their own with an injected fetch. */
export const api = new ApiClient()
