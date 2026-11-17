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
  field?: string
  output?: string
  /** An edit conflict's file as it is on disk now (`edit_conflict` only). */
  current?: unknown
  /** The offset the server actually has (`offset_mismatch` only). */
  received?: number
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

export type SiteKind = "static" | "proxy" | "php"

export interface Site {
  domain: string
  aliases: string[]
  kind: SiteKind
  root?: string
  proxy_target?: string
  php_version: string | null
  /** The FPM pool name of a PHP site. */
  php_pool?: string
  created_at: string
}

/** Whether a site file still includes the file one panel control writes. */
export interface SiteFeature {
  feature: "listen" | "names" | "acme" | "proxy" | "php"
  include: string
  attached: boolean
  reason?: string
}

export interface SiteDetail extends Site {
  site_file: string
  site_file_missing: boolean
  features: SiteFeature[]
}

export interface CreateSiteInput {
  domain: string
  aliases: string[]
  kind: SiteKind
  root?: string
  proxy_target?: string
  /** PHP sites only. */
  php_version?: string
  /** PHP sites only: put a phpinfo() page in a new folder. */
  phpinfo?: boolean
}

export type PHPState = "absent" | "pulling" | "ready" | "failed"

export interface ContainerStatus {
  exists: boolean
  running: boolean
  status: string
  started_at?: string
  error?: string
}

export interface PHPVersion {
  version: string
  image: string
  digest: string
  state: PHPState
  /** The end of a failed pull's output. */
  output: string
  updated_at: string | null
  /** False for a version that left the supported list; it cannot be pulled again. */
  supported: boolean
  container: ContainerStatus
  /** Domains that run on this version. */
  sites: string[]
}

export interface PHPReconcileStep {
  version: string
  sites: number
  ok: boolean
  detail: string
}

export interface PHPList {
  versions: PHPVersion[]
  reconcile: PHPReconcileStep[]
}

/** The php.ini directives the form owns. */
export interface PHPSettings {
  memory_limit: string
  upload_max_filesize: string
  post_max_size: string
  max_execution_time: number
  date_timezone: string
}

export type PoolMode = "static" | "dynamic" | "ondemand"

export interface PoolSettings {
  pm: PoolMode
  max_children: number
  start_servers: number
  min_spare_servers: number
  max_spare_servers: number
  max_requests: number
  idle_timeout: number
}

export interface Pool {
  name: string
  version: string
  socket: string
  file: string
  settings: PoolSettings
}

export interface JobRef {
  id: string
  kind: string
  state: string
}

/* --- File manager (M3 3.10) ------------------------------------------- */

export type FMKind = "file" | "dir" | "symlink" | "other"

export interface FMEntry {
  name: string
  path: string
  kind: FMKind
  size: number
  mode: number
  uid: number
  gid: number
  owner: string
  group: string
  /** Unix nanoseconds. */
  mtime: number
  target?: string
  target_kind?: FMKind
}

export interface FMListing {
  path: string
  entries: FMEntry[]
  total: number
  offset: number
}

export interface FMMount {
  device: string
  path: string
  type: string
  total: number
  used: number
  free: number
}

export interface FMFile {
  path: string
  content: string
  size: number
  mtime: number
  hash: string
  mode: number
}

export interface FMBase {
  size: number
  mtime: number
  hash: string
}

export interface FMTrashItem {
  id: string
  name: string
  orig_path: string
  size: number
  is_dir: boolean
  mode: number
  uid: number
  gid: number
  deleted_at: string
}

export interface FMUploadInfo {
  id: string
  dir: string
  name: string
  size: number
  received: number
  overwrite: boolean
  chunk_max: number
}

/** Error classes only the file manager produces; see handlers_fm.go. */
export type FMErrorCode =
  | "protected_path"
  | "confirmation_required"
  | "too_large"
  | "binary_file"
  | "checksum_mismatch"
  | "no_space"
  | "unsafe_archive"
  | "edit_conflict"
  | "use_config_editor"
  | "offset_mismatch"

export interface FMDownloadFolderOptions {
  format?: "zip" | "tar.gz"
}

export interface UpdateSiteInput {
  aliases?: string[]
  proxy_target?: string
}

/** A change that went live. Warnings did not stop it. */
export interface ApplyResponse {
  warnings: string[]
}

export interface SiteResponse extends ApplyResponse {
  site: Site
}

export interface DeleteSiteResponse extends ApplyResponse {
  web_root: string
}

/** A raw config file the operator edits: a site file or nginx.conf. */
export interface ConfigFile {
  path: string
  content: string
  /** Key of this file under `/api/config-history`. */
  history_file: string
  missing?: boolean
  /** nginx.conf only: what stopped working because an include line is gone. */
  missing_links?: string[]
}

export interface ConfigVersion {
  path: string
  version: number
  created_at: string
  sha256: string
  size: number
  reason: string
}

export interface NginxSettings {
  worker_processes: string
  client_max_body_size: string
  gzip: boolean
  keepalive_timeout: number
  client_body_timeout: number
  client_header_timeout: number
  send_timeout: number
  server_tokens: boolean
}

export interface ReconcileStep {
  name: string
  detail: string
  ok: boolean
}

export interface NginxServiceStatus {
  container: {
    exists: boolean
    running: boolean
    status: string
    started_at?: string
    error?: string
  }
  reconcile: {
    done: boolean
    ok: boolean
    steps: ReconcileStep[]
    at: string
    error?: string
    output?: string
  }
}

/** A failed request. `code` is the envelope's `error`, `detail` its sentence. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly detail: string
  /** The input the server rejected, for an `invalid_input` error. */
  readonly field: string | null
  /** A validator's own text (`nginx -t`), shown verbatim. */
  readonly output: string | null
  /** The raw envelope, for codes that carry extra fields (`edit_conflict`). */
  readonly payload: ErrorEnvelope

  constructor(
    status: number,
    code: string,
    detail: string,
    extra: { field?: string; output?: string; payload?: ErrorEnvelope } = {},
  ) {
    super(detail || code)
    this.name = "ApiError"
    this.status = status
    this.code = code
    this.detail = detail
    this.field = extra.field ?? null
    this.output = extra.output ?? null
    this.payload = extra.payload ?? { error: code, detail }
  }

  get isUnauthorized(): boolean {
    return this.status === 401
  }

  /** The file as it is on disk, for an `edit_conflict`. */
  get conflictCurrent(): FMFile | null {
    const c = this.payload.current
    if (this.code !== "edit_conflict" || typeof c !== "object" || c === null) return null
    return c as FMFile
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

  async sites(): Promise<Site[]> {
    return (await this.request<{ sites: Site[] }>("/api/sites")).sites
  }

  async site(domain: string): Promise<SiteDetail> {
    return this.request<SiteDetail>(`/api/sites/${encodeURIComponent(domain)}`)
  }

  async createSite(input: CreateSiteInput): Promise<SiteResponse> {
    return this.request<SiteResponse>("/api/sites", { method: "POST", body: JSON.stringify(input) })
  }

  /** PUT /api/sites/{domain}. Each field rewrites only its own include file. */
  async updateSite(domain: string, input: UpdateSiteInput): Promise<SiteResponse> {
    return this.request<SiteResponse>(`/api/sites/${encodeURIComponent(domain)}`, {
      method: "PUT",
      body: JSON.stringify(input),
    })
  }

  /** DELETE /api/sites/{domain}. The domain is repeated as the confirmation. */
  async deleteSite(domain: string): Promise<DeleteSiteResponse> {
    const d = encodeURIComponent(domain)
    return this.request<DeleteSiteResponse>(`/api/sites/${d}?confirm=${d}`, { method: "DELETE" })
  }

  async siteFile(domain: string): Promise<ConfigFile> {
    return this.request<ConfigFile>(`/api/sites/${encodeURIComponent(domain)}/file`)
  }

  /** PUT the site file. A 422 `config_rejected` carries nginx's output. */
  async saveSiteFile(domain: string, content: string): Promise<ApplyResponse> {
    return this.request<ApplyResponse>(`/api/sites/${encodeURIComponent(domain)}/file`, {
      method: "PUT",
      body: JSON.stringify({ content }),
    })
  }

  /** Rewrites the site file from the template, discarding the operator's edits. */
  async regenerateSite(domain: string): Promise<ApplyResponse> {
    const d = encodeURIComponent(domain)
    return this.request<ApplyResponse>(`/api/sites/${d}/regenerate?confirm=${d}`, {
      method: "POST",
    })
  }

  async nginxSettings(): Promise<NginxSettings> {
    return this.request<NginxSettings>("/api/nginx/settings")
  }

  async saveNginxSettings(settings: NginxSettings): Promise<NginxSettings> {
    return this.request<NginxSettings>("/api/nginx/settings", {
      method: "PUT",
      body: JSON.stringify(settings),
    })
  }

  async nginxConf(): Promise<ConfigFile> {
    return this.request<ConfigFile>("/api/nginx/conf")
  }

  async saveNginxConf(content: string): Promise<ApplyResponse> {
    return this.request<ApplyResponse>("/api/nginx/conf", {
      method: "PUT",
      body: JSON.stringify({ content }),
    })
  }

  async nginxService(): Promise<NginxServiceStatus> {
    return this.request<NginxServiceStatus>("/api/services/nginx")
  }

  async php(): Promise<PHPList> {
    return this.request<PHPList>("/api/php")
  }

  /** Starts the pull job. The version reads `pulling` until the job ends. */
  async pullPHP(version: string): Promise<JobRef> {
    const res = await this.request<{ job: JobRef }>(
      `/api/php/${encodeURIComponent(version)}/pull`,
      { method: "POST" },
    )
    return res.job
  }

  async phpSettings(version: string): Promise<PHPSettings> {
    return this.request<PHPSettings>(`/api/php/${encodeURIComponent(version)}/settings`)
  }

  async savePHPSettings(version: string, settings: PHPSettings): Promise<PHPSettings> {
    return this.request<PHPSettings>(`/api/php/${encodeURIComponent(version)}/settings`, {
      method: "PUT",
      body: JSON.stringify(settings),
    })
  }

  async phpIni(version: string): Promise<ConfigFile> {
    return this.request<ConfigFile>(`/api/php/${encodeURIComponent(version)}/ini`)
  }

  /** PUT php.ini. A 422 `config_rejected` carries PHP's output. */
  async savePHPIni(version: string, content: string): Promise<ApplyResponse> {
    return this.request<ApplyResponse>(`/api/php/${encodeURIComponent(version)}/ini`, {
      method: "PUT",
      body: JSON.stringify({ content }),
    })
  }

  async sitePool(domain: string): Promise<Pool> {
    return this.request<Pool>(`/api/sites/${encodeURIComponent(domain)}/pool`)
  }

  async saveSitePool(domain: string, settings: PoolSettings): Promise<Pool & ApplyResponse> {
    return this.request<Pool & ApplyResponse>(`/api/sites/${encodeURIComponent(domain)}/pool`, {
      method: "PUT",
      body: JSON.stringify(settings),
    })
  }

  /** Moves a site to another PHP version; on any failure it stays on the old one. */
  async switchPHP(domain: string, version: string): Promise<SiteResponse> {
    return this.request<SiteResponse>(`/api/sites/${encodeURIComponent(domain)}/php-version`, {
      method: "PUT",
      body: JSON.stringify({ version }),
    })
  }

  /** One page of a directory. `hidden` shows dotfiles; `sort` is name, size or mtime. */
  async fmList(input: {
    path: string
    hidden?: boolean
    sort?: "name" | "size" | "mtime"
    desc?: boolean
    offset?: number
    limit?: number
  }): Promise<FMListing> {
    const q = new URLSearchParams({ path: input.path })
    if (input.hidden) q.set("hidden", "1")
    if (input.sort) q.set("sort", input.sort)
    if (input.desc) q.set("desc", "1")
    if (input.offset) q.set("offset", String(input.offset))
    if (input.limit) q.set("limit", String(input.limit))
    return this.request<FMListing>(`/api/fm/list?${q.toString()}`)
  }

  async fmStat(path: string): Promise<FMEntry> {
    return this.request<FMEntry>(`/api/fm/stat?path=${encodeURIComponent(path)}`)
  }

  async fmDisks(): Promise<FMMount[]> {
    const res = await this.request<{ mounts: FMMount[] }>("/api/fm/disks")
    return res.mounts ?? []
  }

  async fmMkdir(path: string): Promise<FMEntry> {
    return this.request<FMEntry>("/api/fm/mkdir", {
      method: "POST",
      body: JSON.stringify({ path }),
    })
  }

  async fmCreateFile(path: string): Promise<FMEntry> {
    return this.request<FMEntry>("/api/fm/create", {
      method: "POST",
      body: JSON.stringify({ path }),
    })
  }

  async fmRename(path: string, name: string): Promise<FMEntry> {
    return this.request<FMEntry>("/api/fm/rename", {
      method: "POST",
      body: JSON.stringify({ path, name }),
    })
  }

  /** Same mount: answers `{moved}`. Across mounts: answers 202 with a job. */
  async fmMove(paths: string[], dest: string): Promise<{ moved: number } | { job: JobRef }> {
    return this.request<{ moved: number } | { job: JobRef }>("/api/fm/move", {
      method: "POST",
      body: JSON.stringify({ paths, dest }),
    })
  }

  /** Always a job: answers 202. */
  async fmCopy(paths: string[], dest: string): Promise<JobRef> {
    const res = await this.request<{ job: JobRef }>("/api/fm/copy", {
      method: "POST",
      body: JSON.stringify({ paths, dest }),
    })
    return res.job
  }

  /** Non-recursive answers `{changed}`; recursive needs `confirm` and answers a job. */
  async fmChmod(input: {
    path: string
    mode: string
    recursive?: boolean
    confirm?: string
  }): Promise<{ changed: number } | { job: JobRef }> {
    return this.request<{ changed: number } | { job: JobRef }>("/api/fm/chmod", {
      method: "POST",
      body: JSON.stringify(input),
    })
  }

  async fmChown(input: {
    path: string
    owner?: string
    group?: string
    recursive?: boolean
    confirm?: string
  }): Promise<{ changed: number } | { job: JobRef }> {
    return this.request<{ changed: number } | { job: JobRef }>("/api/fm/chown", {
      method: "POST",
      body: JSON.stringify(input),
    })
  }

  /**
   * Moves items to the trash. A folder needs `confirm` equal to its base name;
   * a delete that crosses mounts answers 202 with a job instead of the items.
   */
  async fmDelete(
    items: { path: string; confirm?: string }[],
  ): Promise<{ trashed: FMTrashItem[] } | { job: JobRef }> {
    return this.request<{ trashed: FMTrashItem[] } | { job: JobRef }>("/api/fm/delete", {
      method: "POST",
      body: JSON.stringify({ items }),
    })
  }

  async fmTrash(): Promise<FMTrashItem[]> {
    const res = await this.request<{ items: FMTrashItem[] }>("/api/fm/trash")
    return res.items ?? []
  }

  /** Restore answers `{path}`, or 202 with a job when the copy is large. */
  async fmRestore(id: string, name?: string): Promise<{ path: string } | { job: JobRef }> {
    return this.request<{ path: string } | { job: JobRef }>(
      `/api/fm/trash/${encodeURIComponent(id)}/restore`,
      { method: "POST", body: JSON.stringify(name === undefined ? {} : { name }) },
    )
  }

  /** Permanent: `confirm` must equal the item's base name. */
  async fmPurge(id: string, confirm: string): Promise<void> {
    await this.request<void>(
      `/api/fm/trash/${encodeURIComponent(id)}?confirm=${encodeURIComponent(confirm)}`,
      { method: "DELETE" },
    )
  }

  /** Opens a text file for editing. `language` is the server's CodeMirror mode. */
  async fmOpenFile(path: string): Promise<FMFile & { language: string }> {
    return this.request<FMFile & { language: string }>(
      `/api/fm/file?path=${encodeURIComponent(path)}`,
    )
  }

  /**
   * Saves a text file. `base` is what was opened; the server answers 409
   * `edit_conflict` with `current` when the file changed on disk meanwhile.
   */
  async fmSaveFile(path: string, content: string, base: FMBase): Promise<FMFile> {
    return this.request<FMFile>("/api/fm/file", {
      method: "PUT",
      body: JSON.stringify({ path, content, base }),
    })
  }

  async fmUploadStart(input: {
    dir: string
    name: string
    size: number
    overwrite?: boolean
  }): Promise<FMUploadInfo> {
    return this.request<FMUploadInfo>("/api/fm/uploads", {
      method: "POST",
      body: JSON.stringify(input),
    })
  }

  async fmUploadStatus(id: string): Promise<FMUploadInfo> {
    return this.request<FMUploadInfo>(`/api/fm/uploads/${encodeURIComponent(id)}`)
  }

  /** One chunk as raw bytes, with its SHA-256 in `X-Chunk-SHA256`. */
  async fmUploadChunk(
    id: string,
    offset: number,
    chunk: Blob,
    sha256: string,
  ): Promise<FMUploadInfo> {
    const headers = new Headers({
      "Content-Type": "application/octet-stream",
      "X-Chunk-SHA256": sha256,
    })
    if (this.csrfToken !== null) headers.set(CSRF_HEADER, this.csrfToken)
    const res = await this.#fetch(
      `${this.#baseUrl}/api/fm/uploads/${encodeURIComponent(id)}?offset=${offset}`,
      { method: "PUT", headers, body: chunk, credentials: "same-origin" },
    )
    const body = await readBody(res)
    if (!res.ok) {
      const envelope: ErrorEnvelope = isErrorEnvelope(body)
        ? body
        : { error: `http_${res.status}`, detail: res.statusText || "the chunk failed" }
      if (res.status === 401) this.#onUnauthorized?.()
      throw new ApiError(res.status, envelope.error, envelope.detail, { payload: envelope })
    }
    return body as FMUploadInfo
  }

  async fmUploadComplete(id: string, sha256: string): Promise<string> {
    const res = await this.request<{ path: string }>(
      `/api/fm/uploads/${encodeURIComponent(id)}/complete`,
      { method: "POST", body: JSON.stringify({ sha256 }) },
    )
    return res.path
  }

  async fmUploadCancel(id: string): Promise<void> {
    await this.request<void>(`/api/fm/uploads/${encodeURIComponent(id)}`, { method: "DELETE" })
  }

  /** A file downloads as an attachment; a folder streams as an archive. */
  fmDownloadUrl(path: string, opts: FMDownloadFolderOptions = {}): string {
    const q = new URLSearchParams({ path })
    if (opts.format) q.set("format", opts.format)
    return `${this.#baseUrl}/api/fm/download?${q.toString()}`
  }

  /** Always a job. An empty `format` is taken from the destination's extension. */
  async fmArchive(input: {
    dir: string
    names: string[]
    dest: string
    format?: "zip" | "tar.gz" | "tar"
  }): Promise<JobRef> {
    const res = await this.request<{ job: JobRef }>("/api/fm/archive", {
      method: "POST",
      body: JSON.stringify(input),
    })
    return res.job
  }

  /** Always a job. */
  async fmExtract(input: { path: string; dest: string; overwrite?: boolean }): Promise<JobRef> {
    const res = await this.request<{ job: JobRef }>("/api/fm/extract", {
      method: "POST",
      body: JSON.stringify(input),
    })
    return res.job
  }

  /** `file` is a `history_file` key; it contains a slash, so it is escaped whole. */
  async configHistory(file: string): Promise<ConfigVersion[]> {
    const res = await this.request<{ versions: ConfigVersion[] }>(
      `/api/config-history/${encodeURIComponent(file)}`,
    )
    return res.versions
  }

  async configVersion(file: string, version: number): Promise<string> {
    const res = await this.request<{ content: string }>(
      `/api/config-history/${encodeURIComponent(file)}/${version}`,
    )
    return res.content
  }

  /** Restores a version. It is validated like any edit and can be rejected. */
  async restoreConfigVersion(file: string, version: number): Promise<ApplyResponse> {
    return this.request<ApplyResponse>(
      `/api/config-history/${encodeURIComponent(file)}/${version}/restore`,
      { method: "POST" },
    )
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
      const error = new ApiError(res.status, envelope.error, envelope.detail, {
        field: typeof envelope.field === "string" ? envelope.field : undefined,
        output: typeof envelope.output === "string" ? envelope.output : undefined,
        payload: envelope,
      })
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
