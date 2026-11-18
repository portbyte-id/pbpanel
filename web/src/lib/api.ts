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

import type { CronJob, CronJobInput, CronRun, RunResponse } from "@/lib/cron"
import type {
  DockerAction,
  DockerContainer,
  DockerImage,
  DockerInfo,
  DockerLogs,
  DockerProcesses,
  DockerStats,
  DockerVolume,
} from "@/lib/docker"
import type { LogSource } from "@/lib/logs"
import type { ServiceAction, ServiceRow, SysinfoResponse } from "@/lib/sysinfo"
import {
  queryString,
  type TrafficRange,
  type TrafficRequestQuery,
  type TrafficRequests,
  type TrafficSummary,
  type WafConfig,
  type WafEventQuery,
  type WafEvents,
  type WafEventSummary,
  type WafOverview,
  type WafSiteResponse,
} from "@/lib/waf"

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
  /** Display text for each `allowed` value. */
  labels?: Record<string, string>
  /** A string setting that may be left empty. */
  optional?: boolean
  /** Mask the value while typing. */
  secret?: boolean
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
  feature: "listen" | "names" | "acme" | "proxy" | "php" | "tls"
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
  /** The build the version serves, or null when there is none. */
  current: ImageBuild | null
}

/** One build of a PHP version (a container image) or of nginx (a directory on the host). */
export type ImageBuildState = "building" | "ready" | "failed" | "pruned"

export interface ImageBuild {
  id: string
  kind: "php" | "nginx"
  version: string
  tag: string
  image_id: string
  extensions: string[]
  state: ImageBuildState
  /** The end of the build's output; empty when there is none. */
  output: string
  created_at: string
  finished_at: string | null
  current: boolean
  previous: boolean
}

/** One entry of the curated PHP extension catalogue. */
export interface PHPExtension {
  name: string
  module: string
  description: string
  kind: "bundled" | "pecl"
  packages: string[]
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

/* --- TLS (M6) ------------------------------------------------------------ */

export type CertSource = "upload" | "http01" | "dns01"
export type RenewalResult = "" | "ok" | "failed"

/** One site's certificate, as the panel stores it. The private key is never in it. */
export interface CertView {
  domain: string
  source: CertSource
  names: string[]
  not_before: string
  not_after: string
  issuer: string
  serial: string
  days_left: number
  last_renewal_at: string | null
  last_renewal_result: RenewalResult
  last_renewal_error: string
  redirect_http: boolean
  provider: string
}

export interface TLSWarning {
  code: string
  message: string
}

export interface UploadCertInput {
  certificate: string
  chain: string
  private_key: string
}

export type IssueMethod = "http01" | "dns01"

export interface IssueCertInput {
  method: IssueMethod
  names: string[]
  provider?: string
  email?: string
}

export interface DNSProvider {
  name: string
  kind: string
  updated_at: string
}

export interface DNSProviderList {
  providers: DNSProvider[]
  kinds: string[]
}

/** The certificate the panel's own listener serves. */
export interface CertInfo {
  subject: string
  sans: string[]
  issuer: string
  not_before: string
  not_after: string
  serial: string
  self_signed: boolean
}

export interface PanelCert {
  cert: CertInfo
  previous_available: boolean
}

export interface IssuePanelCertInput {
  hostname: string
  method: IssueMethod
  provider?: string
  email?: string
}

/* --- Apps and runtimes (M5)--------------------------------------------- */

export type AppType = "go" | "node" | "command"
export type AppRestart = "always" | "on-failure" | "never"

/** An app as the server reports it: its definition and what supervisord says. */
export interface PanelApp {
  id: number
  name: string
  type: AppType
  command: string
  args: string[]
  workdir: string
  env: Record<string, string>
  user: string
  node_version: string
  autostart: boolean
  restart: AppRestart
  port: number | null
  created_at: string
  /** supervisord's state name, "unknown" when it could not be asked, "not loaded" when it has no such program. */
  state: string
  pid: number
  uptime_seconds: number
  exitstatus: number
  spawn_error: string
  /** `http://127.0.0.1:<port>`, empty when the app has no port. */
  proxy_url: string
}

export interface AppInput {
  name: string
  type: AppType
  command: string
  args?: string[]
  workdir?: string
  env?: Record<string, string>
  user?: string
  node_version?: string
  autostart?: boolean
  restart?: AppRestart
  port?: number | null
}

export interface AppResponse {
  app: PanelApp
  warnings: string[]
}

export type AppAction = "start" | "stop" | "restart"

export interface LogChunk {
  data: string
  /** Where to continue reading. */
  offset: number
}

export interface NodeVersion {
  version: string
  path: string
  used_by: string[]
}

export interface RuntimesState {
  nvm_installed: boolean
  nvm_tag: string
  node: NodeVersion[]
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

/** The nginx builds: what they are built from, the build in use, and its history. */
export interface NginxImage {
  /** What every build is compiled from, for display. */
  base: string
  /** A Docker-style reference from before nginx ran natively. The page does not show it. */
  repo: string
  /** The nginx version that serves, and the versions the page offers. */
  version: string
  versions: string[]
  /** True when nginx was stopped from the panel and is left stopped. */
  stopped: boolean
  present: boolean
  /** True while an image build is running. */
  busy: boolean
  current: ImageBuild | null
  previous: ImageBuild | null
  builds: ImageBuild[]
}

export type FirewallProtocol = "tcp" | "udp" | "both"

export interface FirewallPending {
  token: string
  startedAt: string
  deadline: string
}

export interface FirewallState {
  enabled: boolean
  ipv6: boolean
  /** Stored rules differ from the live, confirmed ruleset. */
  dirty: boolean
  sshPorts: number[]
  panelPort: number
  pending: FirewallPending | null
  /** Non-panel containers publishing ports; Docker bypasses the panel's rules for these. */
  dockerPorts: { container: string; port: number; protocol: string }[]
  builtins: { label: string; detail: string }[]
}

export interface FirewallRule {
  id: string
  portStart: number
  portEnd: number
  protocol: FirewallProtocol
  source?: string
  comment?: string
  enabled: boolean
}

export type FirewallRuleInput = Omit<FirewallRule, "id">

export interface BlocklistEntry {
  id: string
  addr: string
  comment?: string
}

export interface AllowlistEntry {
  id: string
  addr: string
  comment?: string
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

  async siteWaf(domain: string): Promise<WafSiteResponse> {
    return this.request<WafSiteResponse>(`/api/sites/${encodeURIComponent(domain)}/waf`)
  }

  /** PUT /api/sites/{domain}/waf. The whole config is replaced and `nginx -t` runs first. */
  async saveSiteWaf(domain: string, config: WafConfig): Promise<WafSiteResponse> {
    return this.request<WafSiteResponse>(`/api/sites/${encodeURIComponent(domain)}/waf`, {
      method: "PUT",
      body: JSON.stringify(config),
    })
  }

  async wafOverview(): Promise<WafOverview> {
    return this.request<WafOverview>("/api/waf/status")
  }

  async wafTraffic(site: string, range: TrafficRange): Promise<TrafficSummary> {
    return this.request<TrafficSummary>(`/api/waf/traffic${queryString({ site, range })}`)
  }

  async wafRequests(query: TrafficRequestQuery): Promise<TrafficRequests> {
    return this.request<TrafficRequests>(`/api/waf/requests${queryString({ ...query })}`)
  }

  async wafEvents(query: WafEventQuery): Promise<WafEvents> {
    return this.request<WafEvents>(`/api/waf/events${queryString({ ...query })}`)
  }

  async wafEventSummary(site: string, range: TrafficRange): Promise<WafEventSummary> {
    return this.request<WafEventSummary>(`/api/waf/events/summary${queryString({ site, range })}`)
  }

  /** GET /api/sites/{domain}/tls. Null while the site has no certificate. */
  async siteTLS(domain: string): Promise<CertView | null> {
    const res = await this.request<{ cert: CertView | null }>(tlsPath(domain))
    return res.cert
  }

  async uploadSiteCert(
    domain: string,
    input: UploadCertInput,
  ): Promise<{ cert: CertView; warnings: TLSWarning[] }> {
    return this.request<{ cert: CertView; warnings: TLSWarning[] }>(`${tlsPath(domain)}/upload`, {
      method: "POST",
      body: JSON.stringify(input),
    })
  }

  /** Starts an issuance job and returns its id; the certificate appears when it succeeds. */
  async issueSiteCert(domain: string, input: IssueCertInput): Promise<string> {
    const res = await this.request<{ job_id: string }>(`${tlsPath(domain)}/issue`, {
      method: "POST",
      body: JSON.stringify(input),
    })
    return res.job_id
  }

  async renewSiteCert(domain: string): Promise<string> {
    const res = await this.request<{ job_id: string }>(`${tlsPath(domain)}/renew`, {
      method: "POST",
    })
    return res.job_id
  }

  async setSiteRedirect(domain: string, redirectHTTP: boolean): Promise<CertView> {
    return this.request<CertView>(`${tlsPath(domain)}/redirect`, {
      method: "PUT",
      body: JSON.stringify({ redirect_http: redirectHTTP }),
    })
  }

  async removeSiteCert(domain: string): Promise<void> {
    await this.request<void>(tlsPath(domain), { method: "DELETE" })
  }

  async tlsCerts(): Promise<CertView[]> {
    return (await this.request<{ certs: CertView[] }>("/api/tls/certs")).certs
  }

  async tlsProviders(): Promise<DNSProviderList> {
    return this.request<DNSProviderList>("/api/tls/providers")
  }

  /** PUT adds the provider or replaces its credentials. The token is never read back. */
  async saveTLSProvider(name: string, kind: string, apiToken: string): Promise<void> {
    await this.request<unknown>(`/api/tls/providers/${encodeURIComponent(name)}`, {
      method: "PUT",
      body: JSON.stringify({ kind, credentials: { api_token: apiToken } }),
    })
  }

  async deleteTLSProvider(name: string): Promise<void> {
    await this.request<void>(`/api/tls/providers/${encodeURIComponent(name)}`, {
      method: "DELETE",
    })
  }

  async panelCert(): Promise<PanelCert> {
    return this.request<PanelCert>("/api/panel/cert")
  }

  async uploadPanelCert(input: {
    certificate: string
    chain?: string
    private_key: string
  }): Promise<CertInfo> {
    return this.request<CertInfo>("/api/panel/cert/upload", {
      method: "POST",
      body: JSON.stringify(input),
    })
  }

  async issuePanelCert(input: IssuePanelCertInput): Promise<string> {
    const res = await this.request<{ job_id: string }>("/api/panel/cert/issue", {
      method: "POST",
      body: JSON.stringify(input),
    })
    return res.job_id
  }

  async panelIP(): Promise<{ detected: string; reason?: string; current: string }> {
    return this.request("/api/panel/cert/ip")
  }

  async issuePanelIPCert(input: { ip: string; email?: string }): Promise<string> {
    const res = await this.request<{ job_id: string }>("/api/panel/cert/ip", {
      method: "POST",
      body: JSON.stringify(input),
    })
    return res.job_id
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

  async nginxImage(): Promise<NginxImage> {
    return this.request<NginxImage>("/api/nginx/image")
  }

  /** Builds a new image from the newest nginx base and swaps it in once `nginx -t` passes. */
  async updateNginxImage(): Promise<JobRef> {
    const res = await this.request<{ job: JobRef }>("/api/nginx/image/update", { method: "POST" })
    return res.job
  }

  /** Starts, stops or restarts the nginx service as a job. */
  async nginxContainerAction(action: "start" | "stop" | "restart"): Promise<JobRef> {
    const res = await this.request<{ job: JobRef }>(`/api/nginx/container/${action}`, { method: "POST" })
    return res.job
  }

  /** Switches nginx to another version: builds it, checks the live config, swaps. */
  async setNginxVersion(version: string): Promise<JobRef> {
    const res = await this.request<{ job: JobRef }>("/api/nginx/image/version", {
      method: "POST",
      body: JSON.stringify({ version }),
    })
    return res.job
  }

  async activateNginxImageBuild(buildId: string): Promise<JobRef> {
    const res = await this.request<{ job: JobRef }>(
      `/api/nginx/image/builds/${encodeURIComponent(buildId)}/activate`,
      { method: "POST" },
    )
    return res.job
  }

  async php(): Promise<PHPList> {
    return this.request<PHPList>("/api/php")
  }

  /**
   * Starts the pull job. The version reads `pulling` until the job ends. Without
   * `extensions` the current build's extensions are kept, as before.
   */
  async pullPHP(version: string, extensions?: string[]): Promise<JobRef> {
    const res = await this.request<{ job: JobRef }>(
      `/api/php/${encodeURIComponent(version)}/pull`,
      extensions === undefined
        ? { method: "POST" }
        : { method: "POST", body: JSON.stringify({ extensions }) },
    )
    return res.job
  }

  /** The curated extension catalogue, sorted by name. */
  async phpExtensions(): Promise<PHPExtension[]> {
    const res = await this.request<{ extensions: PHPExtension[] | null }>("/api/php/extensions")
    return (res.extensions ?? []).map((e) => ({ ...e, packages: e.packages ?? [] }))
  }

  /** Rebuilds a ready version with exactly these extensions. It serves until the build passes. */
  async buildPHP(version: string, extensions: string[]): Promise<JobRef> {
    const res = await this.request<{ job: JobRef }>(
      `/api/php/${encodeURIComponent(version)}/build`,
      { method: "POST", body: JSON.stringify({ extensions }) },
    )
    return res.job
  }

  /** Removes the container and the version's images; config and settings are kept. */
  async removePHP(version: string): Promise<{ version: string; state: string }> {
    return this.request<{ version: string; state: string }>(
      `/api/php/${encodeURIComponent(version)}`,
      { method: "DELETE" },
    )
  }

  /** Newest first. */
  async phpBuilds(version: string): Promise<ImageBuild[]> {
    const res = await this.request<{ builds: ImageBuild[] | null }>(
      `/api/php/${encodeURIComponent(version)}/builds`,
    )
    return (res.builds ?? []).map((b) => ({ ...b, extensions: b.extensions ?? [] }))
  }

  /** Makes a ready build the one the version serves. This is the rollback. */
  async activatePHPBuild(version: string, buildId: string): Promise<JobRef> {
    const res = await this.request<{ job: JobRef }>(
      `/api/php/${encodeURIComponent(version)}/builds/${encodeURIComponent(buildId)}/activate`,
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

  async apps(): Promise<PanelApp[]> {
    return (await this.request<{ apps: PanelApp[] }>("/api/apps")).apps
  }

  async createApp(input: AppInput): Promise<AppResponse> {
    return this.request<AppResponse>("/api/apps", { method: "POST", body: JSON.stringify(input) })
  }

  /** The name cannot change: it is the program name supervisord knows. */
  async updateApp(name: string, input: AppInput): Promise<AppResponse> {
    return this.request<AppResponse>(`/api/apps/${encodeURIComponent(name)}`, {
      method: "PUT",
      body: JSON.stringify(input),
    })
  }

  async deleteApp(name: string): Promise<void> {
    return this.request<void>(`/api/apps/${encodeURIComponent(name)}`, { method: "DELETE" })
  }

  async controlApp(name: string, action: AppAction): Promise<AppResponse> {
    return this.request<AppResponse>(`/api/apps/${encodeURIComponent(name)}/${action}`, {
      method: "POST",
      body: "{}",
    })
  }

  /** No offset reads the tail; an offset reads on from there. */
  async appLog(name: string, offset?: number): Promise<LogChunk> {
    const q = offset === undefined ? "" : `?offset=${offset}`
    return this.request<LogChunk>(`/api/apps/${encodeURIComponent(name)}/logs${q}`)
  }

  async runtimes(): Promise<RuntimesState> {
    return this.request<RuntimesState>("/api/runtimes")
  }

  /** Always a job: nvm downloads and unpacks Node.js. */
  async installNode(version: string): Promise<JobRef> {
    const res = await this.request<{ job: JobRef }>("/api/runtimes/node", {
      method: "POST",
      body: JSON.stringify({ version }),
    })
    return res.job
  }

  async uninstallNode(version: string): Promise<JobRef> {
    const res = await this.request<{ job: JobRef }>(
      `/api/runtimes/node/${encodeURIComponent(version)}`,
      { method: "DELETE" },
    )
    return res.job
  }

  async firewall(): Promise<FirewallState> {
    return this.request<FirewallState>("/api/firewall")
  }

  async firewallRules(): Promise<FirewallRule[]> {
    return (await this.request<{ rules: FirewallRule[] }>("/api/firewall/rules")).rules
  }

  async createFirewallRule(input: FirewallRuleInput): Promise<FirewallRule> {
    return this.request<FirewallRule>("/api/firewall/rules", {
      method: "POST",
      body: JSON.stringify(input),
    })
  }

  async updateFirewallRule(id: string, input: FirewallRuleInput): Promise<FirewallRule> {
    return this.request<FirewallRule>(`/api/firewall/rules/${encodeURIComponent(id)}`, {
      method: "PUT",
      body: JSON.stringify(input),
    })
  }

  async deleteFirewallRule(id: string): Promise<void> {
    return this.request<void>(`/api/firewall/rules/${encodeURIComponent(id)}`, {
      method: "DELETE",
    })
  }

  async firewallBlocklist(): Promise<BlocklistEntry[]> {
    return (await this.request<{ entries: BlocklistEntry[] }>("/api/firewall/blocklist")).entries
  }

  async addBlocklistEntry(input: { addr: string; comment?: string }): Promise<BlocklistEntry> {
    return this.request<BlocklistEntry>("/api/firewall/blocklist", {
      method: "POST",
      body: JSON.stringify(input),
    })
  }

  async removeBlocklistEntry(id: string): Promise<void> {
    return this.request<void>(`/api/firewall/blocklist/${encodeURIComponent(id)}`, {
      method: "DELETE",
    })
  }

  async firewallAllowlist(): Promise<AllowlistEntry[]> {
    return (await this.request<{ entries: AllowlistEntry[] }>("/api/firewall/allowlist")).entries
  }

  async addAllowlistEntry(input: { addr: string; comment?: string }): Promise<AllowlistEntry> {
    return this.request<AllowlistEntry>("/api/firewall/allowlist", {
      method: "POST",
      body: JSON.stringify(input),
    })
  }

  async removeAllowlistEntry(id: string): Promise<void> {
    return this.request<void>(`/api/firewall/allowlist/${encodeURIComponent(id)}`, {
      method: "DELETE",
    })
  }

  /** First enable: the server stops and masks ufw, then applies with the confirm timer. */
  async enableFirewall(): Promise<FirewallPending> {
    return this.request<FirewallPending>("/api/firewall/enable", { method: "POST", body: "{}" })
  }

  async applyFirewall(): Promise<FirewallPending> {
    return this.request<FirewallPending>("/api/firewall/apply", { method: "POST", body: "{}" })
  }

  async confirmFirewall(token: string): Promise<void> {
    return this.request<void>("/api/firewall/confirm", {
      method: "POST",
      body: JSON.stringify({ token }),
    })
  }

  async revertFirewall(): Promise<void> {
    return this.request<void>("/api/firewall/revert", { method: "POST", body: "{}" })
  }

  async disableFirewall(): Promise<void> {
    return this.request<void>("/api/firewall/disable", { method: "POST", body: "{}" })
  }

  /* --- Dashboard, logs and cron (M7) ------------------------------------- */

  /** `latest` is null and `history` empty before the first sample. */
  async sysinfo(limit?: number): Promise<SysinfoResponse> {
    const q = limit === undefined ? "" : `?limit=${limit}`
    return this.request<SysinfoResponse>(`/api/sysinfo${q}`)
  }

  async services(): Promise<ServiceRow[]> {
    return (await this.request<{ services: ServiceRow[] }>("/api/services")).services
  }

  /** 204. The name is the row's `name` verbatim (`app:mysite` for an app). */
  async controlService(name: string, action: ServiceAction): Promise<void> {
    await this.request<void>(`/api/services/${encodeURIComponent(name)}/${action}`, {
      method: "POST",
      body: "{}",
    })
  }

  async logSources(): Promise<LogSource[]> {
    return (await this.request<{ sources: LogSource[] }>("/api/logs")).sources
  }

  /* --- Docker ------------------------------------------------------------ */

  async dockerInfo(): Promise<DockerInfo> {
    return this.request<DockerInfo>("/api/docker")
  }

  async dockerContainers(): Promise<DockerContainer[]> {
    return (await this.request<{ containers: DockerContainer[] }>("/api/docker/containers")).containers
  }

  async dockerStats(): Promise<DockerStats[]> {
    return (await this.request<{ stats: DockerStats[] }>("/api/docker/stats")).stats
  }

  async dockerProcesses(id: string): Promise<DockerProcesses> {
    return this.request<DockerProcesses>(`/api/docker/containers/${encodeURIComponent(id)}/top`)
  }

  async dockerLogs(id: string, tail = 200): Promise<DockerLogs> {
    return this.request<DockerLogs>(`/api/docker/containers/${encodeURIComponent(id)}/logs?tail=${tail}`)
  }

  async dockerControl(id: string, action: DockerAction): Promise<void> {
    await this.request<unknown>(`/api/docker/containers/${encodeURIComponent(id)}/${action}`, {
      method: "POST",
      body: "{}",
    })
  }

  async dockerRemoveContainer(id: string, force: boolean): Promise<void> {
    await this.request<unknown>(
      `/api/docker/containers/${encodeURIComponent(id)}${force ? "?force=true" : ""}`,
      { method: "DELETE" },
    )
  }

  async dockerImages(): Promise<DockerImage[]> {
    return (await this.request<{ images: DockerImage[] }>("/api/docker/images")).images
  }

  async dockerRemoveImage(id: string): Promise<void> {
    await this.request<unknown>(`/api/docker/images/${encodeURIComponent(id)}`, { method: "DELETE" })
  }

  async dockerVolumes(): Promise<DockerVolume[]> {
    return (await this.request<{ volumes: DockerVolume[] }>("/api/docker/volumes")).volumes
  }

  async dockerRemoveVolume(name: string): Promise<void> {
    await this.request<unknown>(`/api/docker/volumes/${encodeURIComponent(name)}`, { method: "DELETE" })
  }

  async cronJobs(): Promise<CronJob[]> {
    return (await this.request<{ jobs: CronJob[] }>("/api/cron")).jobs
  }

  async createCronJob(input: CronJobInput): Promise<CronJob> {
    return this.request<CronJob>("/api/cron", { method: "POST", body: JSON.stringify(input) })
  }

  /** PUT replaces the definition. The name cannot change. */
  async updateCronJob(id: number, input: CronJobInput): Promise<CronJob> {
    return this.request<CronJob>(`/api/cron/${id}`, { method: "PUT", body: JSON.stringify(input) })
  }

  async deleteCronJob(id: number): Promise<void> {
    await this.request<void>(`/api/cron/${id}`, { method: "DELETE" })
  }

  async setCronEnabled(id: number, enabled: boolean): Promise<CronJob> {
    return this.request<CronJob>(`/api/cron/${id}/${enabled ? "enable" : "disable"}`, {
      method: "POST",
      body: "{}",
    })
  }

  /** 202 for started and queued, 200 for skipped; both resolve to the outcome. */
  async runCronJob(id: number): Promise<RunResponse> {
    return this.request<RunResponse>(`/api/cron/${id}/run`, { method: "POST", body: "{}" })
  }

  async cronRuns(id: number, limit?: number): Promise<CronRun[]> {
    const q = limit === undefined ? "" : `?limit=${limit}`
    return (await this.request<{ runs: CronRun[] }>(`/api/cron/${id}/runs${q}`)).runs
  }

  async cronRun(id: number, runId: number): Promise<CronRun> {
    return this.request<CronRun>(`/api/cron/${id}/runs/${runId}`)
  }

  /**
   * A GET whose body is read as a stream (a log tail, a search). The envelope,
   * the 401 handling and `ApiError` are the same as `request`; on success the
   * caller gets the live `Response`. `signal` is how the caller stops it.
   */
  async openStream(path: string, accept: string, signal?: AbortSignal): Promise<Response> {
    const res = await this.#fetch(this.#baseUrl + path, {
      method: "GET",
      headers: { Accept: accept },
      credentials: "same-origin",
      signal,
    })
    if (res.ok) return res
    const body = await readBody(res)
    const envelope: ErrorEnvelope = isErrorEnvelope(body)
      ? body
      : { error: `http_${res.status}`, detail: res.statusText || "the request failed" }
    const error = new ApiError(res.status, envelope.error, envelope.detail, {
      field: typeof envelope.field === "string" ? envelope.field : undefined,
      output: typeof envelope.output === "string" ? envelope.output : undefined,
      payload: envelope,
    })
    if (error.isUnauthorized) this.#onUnauthorized?.()
    throw error
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

function tlsPath(domain: string): string {
  return `/api/sites/${encodeURIComponent(domain)}/tls`
}

function isLoginPath(path: string): boolean {
  return path.split("?")[0] === "/api/login"
}

/** The client the app uses. Tests construct their own with an injected fetch. */
export const api = new ApiClient()
