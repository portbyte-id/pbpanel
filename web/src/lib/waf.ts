/**
 * The firewall (WAF) contract: per-site protection settings, the traffic monitor
 * and the ModSecurity audit events. Types mirror the server's JSON; the helpers
 * hold the client-side limits so a bad value is caught before a config apply.
 */

export type WafMode = "off" | "detect" | "block"
export type WafExclusionKind = "rule" | "rule_path" | "rule_arg"

/**
 * The inspection engine a site's firewall runs. A row stored before the field
 * existed carries "", which the server reads as ModSecurity and which this
 * client does too (`engineOf`), so an older panel's JSON still renders.
 */
export type WafEngine = "modsecurity" | "open-appsec"

/**
 * The engine a config or overview row runs, with the server's own reading of a
 * missing value: "" (a row stored before the field existed) and absent (an older
 * panel's overview row) both read as ModSecurity.
 */
export function engineOf(c: { engine?: WafEngine | "" }): WafEngine {
  return c.engine === "open-appsec" ? "open-appsec" : "modsecurity"
}

export const ENGINE_LABEL: Record<WafEngine, string> = {
  modsecurity: "ModSecurity",
  "open-appsec": "open-appsec",
}

/**
 * The lowest threat score that blocks, for open-appsec's prevent-learn mode. A
 * lower level blocks more: `critical` only the certain attacks, `medium` also
 * the doubtful ones.
 */
export type WafConfidence = "critical" | "high" | "medium"

export const CONFIDENCE_OPTIONS: { value: WafConfidence; label: string; help: string }[] = [
  {
    value: "critical",
    label: "Critical only",
    help: "Blocks only attacks the engine is certain about. The fewest false positives.",
  },
  {
    value: "high",
    label: "High and above",
    help: "Blocks high-confidence attacks too. The default, and the balance the vendor recommends.",
  },
  {
    value: "medium",
    label: "Medium and above",
    help: "Also blocks the doubtful ones. Expect false positives on unusual applications.",
  },
]

/**
 * What an open-appsec exception narrows. `path` is deliberately absent from the
 * controls: step 0 and step 4 both measured that a `urlPath` exception does not
 * compile on the installed agent (docs/design/open-appsec-step4-findings.md §C),
 * and a control that silently matches nothing is worse than none. The kind stays
 * in the type because a stored row may carry it; the form never offers it and
 * says why when a row has one.
 */
export type WafExceptionKind = "path" | "source_ip" | "param"

/** The kinds the form offers, in order. */
export const EXCEPTION_KINDS: WafExceptionKind[] = ["source_ip", "param"]

export const EXCEPTION_KIND_LABEL: Record<WafExceptionKind, string> = {
  path: "Path prefix",
  source_ip: "Source network",
  param: "Request parameter",
}

export interface WafOpenAppsecException {
  kind: WafExceptionKind
  path: string
  source_ip: string
  param: string
  note: string
}

export interface WafOpenAppsec {
  confidence: WafConfidence
  exceptions: WafOpenAppsecException[]
}

export interface WafRateLimit {
  enabled: boolean
  requests_per_second: number
  burst: number
}

export interface WafExclusion {
  kind: WafExclusionKind
  rule_id: number
  path: string
  arg: string
  note: string
}

export type ChallengeProvider = "pow" | "turnstile"

/** The page a visitor passes before the site opens. */
export interface WafChallenge {
  enabled: boolean
  provider: ChallengeProvider
  /** Leading zero bits the proof of work needs. */
  difficulty: number
  clearance_minutes: number
  exempt_paths: string[]
  exempt_ips: string[]
}

export interface WafConfig {
  /**
   * The inspection engine. "" is a row stored before the field existed; the
   * server reads it as ModSecurity and `engineOf` reads it the same way.
   */
  engine: WafEngine | ""
  mode: WafMode
  paranoia: 1 | 2 | 3 | 4
  rate_limit: WafRateLimit
  path_blocklist: string[]
  ip_rules: { allow: string[]; deny: string[] }
  exclusions: WafExclusion[]
  /** open-appsec's own settings. They stay on the config whatever the engine. */
  open_appsec: WafOpenAppsec
  challenge: WafChallenge
}

export interface WafSiteStatus {
  available: boolean
  flavor: "pqc" | "standard" | "unknown"
  reason: string
  attached: boolean
  crs_version: string
  /** The challenge needs the panel's secret key. */
  challenge_available?: boolean
  /** Settings holds both the Turnstile site key and secret key. */
  turnstile_ready?: boolean
}

export interface WafSiteResponse {
  config: WafConfig
  status: WafSiteStatus
  /**
   * The second engine's state and its tracking warning. Absent on a panel with
   * no appsec package, which is how a client tells "no engine" from "engine,
   * unavailable".
   */
  open_appsec?: import("@/lib/appsec").AppsecAvailability
  warnings?: string[]
}

export interface WafOverviewSite {
  domain: string
  /** The inspection engine this site runs. Absent from an older panel. */
  engine?: WafEngine | ""
  mode: WafMode
  paranoia: number
  rate_limit: boolean
  path_blocklist: number
  ip_rules: number
  exclusions: number
  /** The challenge provider, or "" when the challenge is off. */
  challenge?: ChallengeProvider | ""
}

export interface WafOverview {
  available: boolean
  flavor: string
  reason: string
  sites: WafOverviewSite[]
  /** The second engine's state, the same value the Store card carries. */
  open_appsec?: import("@/lib/appsec").AppsecAvailability
}

export type TrafficRange = "5m" | "15m" | "1h"

export interface TrafficTotals {
  requests: number
  bytes: number
  s2xx: number
  s3xx: number
  s4xx: number
  s5xx: number
  waf_events: number
  avg_ms: number
  max_ms: number
}

export interface TrafficPoint {
  t: number
  requests: number
  s2xx: number
  s3xx: number
  s4xx: number
  s5xx: number
  bytes: number
  avg_ms: number
  waf_events: number
}

export interface TrafficSummary {
  from: number | string
  to: number | string
  step_seconds: number
  rps_now: number
  totals: TrafficTotals
  series: TrafficPoint[]
  top_ips: { ip: string; requests: number; errors: number; bytes: number }[]
  top_paths: { path: string; requests: number; errors: number }[]
  top_statuses: { status: number; requests: number }[]
  top_agents: { agent: string; requests: number }[]
  sites: { domain: string; requests: number; errors: number; waf_events: number }[]
}

export interface TrafficRequest {
  seq: number
  time: string | number
  site: string
  ip: string
  method: string
  path: string
  status: number
  bytes: number
  ms: number
  referer: string
  agent: string
  request_id: string
}

export interface TrafficRequests {
  requests: TrafficRequest[]
  latest_seq: number
}

export interface TrafficRequestQuery {
  site?: string
  after?: number
  limit?: number
  status?: string
  ip?: string
  q?: string
}

export type WafAction = "blocked" | "detected"

export interface WafMatch {
  rule_id: number
  message: string
  severity: string
  data: string
  tags: string[]
  file: string
  line: number
}

export interface WafEvent {
  seq: number
  time: string | number
  site: string
  /**
   * The engine that produced the event. A ModSecurity event carries
   * "modsecurity" and an open-appsec one "open-appsec"; absent from an older
   * panel, which only had the first.
   */
  engine?: WafEngine | ""
  ip: string
  method: string
  uri: string
  status: number
  request_id: string
  action: WafAction
  rule_id: number
  message: string
  severity: string
  matches: WafMatch[]
  /**
   * open-appsec's own fields. A ModSecurity event leaves them empty, and an
   * open-appsec event leaves `rule_id` 0, because the agent has no rule ids.
   *
   * `confidence` is the engine's own certainty ("Very High"), `attack_type`
   * what it says fired ("SQL Injection"), and `parameter` the request field
   * that matched with where ("id (url parameter)").
   */
  confidence?: string
  attack_type?: string
  parameter?: string
}

export interface WafEvents {
  events: WafEvent[]
  next_before: number
  total: number
}

export interface WafEventQuery {
  site?: string
  ip?: string
  rule?: string
  severity?: string
  action?: WafAction | ""
  q?: string
  limit?: number
  before?: number
}

export interface WafEventSummary {
  top_rules: { rule_id: number; message: string; severity: string; count: number; last: string | number }[]
  by_site: { domain: string; count: number }[]
  by_action: { blocked: number; detected: number }
}

/** A query string from the non-empty entries, so an unset filter is not sent. */
export function queryString(params: Record<string, string | number | undefined | null>): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === "") continue
    q.set(k, String(v))
  }
  const s = q.toString()
  return s === "" ? "" : `?${s}`
}

export const DEFAULT_WAF_CONFIG: WafConfig = {
  engine: "modsecurity",
  mode: "off",
  paranoia: 1,
  rate_limit: { enabled: false, requests_per_second: 10, burst: 20 },
  path_blocklist: [],
  ip_rules: { allow: [], deny: [] },
  exclusions: [],
  open_appsec: { confidence: "high", exceptions: [] },
  challenge: {
    enabled: false,
    provider: "pow",
    difficulty: 16,
    clearance_minutes: 60,
    exempt_paths: [],
    exempt_ips: [],
  },
}

/** Server limits, mirrored so the form can refuse before a request. */
export const WAF_LIMITS = {
  paths: 200,
  ips: 500,
  exclusions: 200,
  note: 200,
  maxRuleId: 9_999_999,
  minDifficulty: 8,
  maxDifficulty: 24,
} as const

/** The proof-of-work levels offered, with a rough time on a phone. */
export const DIFFICULTY_OPTIONS: { value: number; label: string }[] = [
  { value: 12, label: "Light (12): instant" },
  { value: 16, label: "Normal (16): under a second" },
  { value: 18, label: "Firm (18): about a second" },
  { value: 20, label: "Hard (20): a few seconds" },
  { value: 22, label: "Under attack (22): 10 seconds or more" },
]

export const CLEARANCE_OPTIONS: { value: number; label: string }[] = [
  { value: 15, label: "15 minutes" },
  { value: 60, label: "1 hour" },
  { value: 240, label: "4 hours" },
  { value: 1440, label: "1 day" },
  { value: 10080, label: "7 days" },
]

const PATH_RE = /^\/[A-Za-z0-9/_.~%-]*$/
const ARG_RE = /^[A-Za-z0-9_.\-[\]]{1,64}$/
const V4_RE = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})(?:\/(\d{1,2}))?$/
const V6_RE = /^[0-9A-Fa-f:.]+(?:\/\d{1,3})?$/

export function isValidPath(p: string): boolean {
  return PATH_RE.test(p)
}

export function isValidArg(a: string): boolean {
  return ARG_RE.test(a)
}

/** An IPv4/IPv6 address or CIDR. Deliberately loose for v6; the server is exact. */
export function isValidIPEntry(entry: string): boolean {
  const v4 = V4_RE.exec(entry)
  if (v4) {
    if (v4.slice(1, 5).some((o) => Number(o) > 255)) return false
    return v4[5] === undefined || Number(v4[5]) <= 32
  }
  if (!entry.includes(":") || !V6_RE.test(entry)) return false
  const slash = entry.indexOf("/")
  return slash < 0 || Number(entry.slice(slash + 1)) <= 128
}

export function isValidRuleId(n: number): boolean {
  return Number.isInteger(n) && n >= 1 && n <= WAF_LIMITS.maxRuleId
}

/** Why an exclusion cannot be added, or null. */
export function exclusionProblem(e: WafExclusion): string | null {
  if (!isValidRuleId(e.rule_id)) return `Rule id must be a whole number from 1 to ${WAF_LIMITS.maxRuleId}.`
  if (e.kind !== "rule" && !isValidPath(e.path)) {
    return "Path must start with / and use only letters, digits and / _ . ~ % -."
  }
  if (e.kind === "rule_arg" && !isValidArg(e.arg)) {
    return "Argument name may use letters, digits and _ . - [ ], up to 64 characters."
  }
  if (e.note.length > WAF_LIMITS.note) return `Note is limited to ${WAF_LIMITS.note} characters.`
  return null
}

/** Why an open-appsec exception cannot be added, or null. */
export function exceptionProblem(e: WafOpenAppsecException): string | null {
  switch (e.kind) {
    case "path":
      // Not offered, and not savable: the agent does not compile a urlPath
      // exception, so accepting one would be a control that does nothing.
      return "Path exceptions are not supported by the open-appsec agent and cannot be saved."
    case "source_ip":
      if (!isValidIPEntry(e.source_ip)) return "Not an IP address or CIDR range."
      break
    case "param":
      if (!isValidArg(e.param)) {
        return "Parameter name may use letters, digits and _ . - [ ], up to 64 characters."
      }
      break
    default:
      return "Choose a source network or a request parameter."
  }
  if (e.note.length > WAF_LIMITS.note) return `Note is limited to ${WAF_LIMITS.note} characters.`
  return null
}

/** Field → message for everything the whole-config PUT would refuse, mirrored client-side. */
export function configProblems(c: WafConfig): Record<string, string> {
  const out: Record<string, string> = {}
  const rl = c.rate_limit
  if (rl.enabled) {
    if (!Number.isInteger(rl.requests_per_second) || rl.requests_per_second < 1 || rl.requests_per_second > 10_000) {
      out.requests_per_second = "Requests per second must be a whole number from 1 to 10000."
    }
    if (!Number.isInteger(rl.burst) || rl.burst < 0 || rl.burst > 100_000) {
      out.burst = "Burst must be a whole number from 0 to 100000."
    }
  }
  if (c.path_blocklist.length > WAF_LIMITS.paths) out.path_blocklist = `At most ${WAF_LIMITS.paths} paths.`
  for (const side of ["allow", "deny"] as const) {
    if (c.ip_rules[side].length > WAF_LIMITS.ips) out[`ip_${side}`] = `At most ${WAF_LIMITS.ips} entries.`
  }
  if (c.exclusions.length > WAF_LIMITS.exclusions) out.exclusions = `At most ${WAF_LIMITS.exclusions} exclusions.`
  const oa = c.open_appsec
  if (!CONFIDENCE_OPTIONS.some((o) => o.value === oa.confidence)) {
    out.open_appsec_confidence = "Choose critical, high or medium."
  }
  if (oa.exceptions.length > WAF_LIMITS.exclusions) {
    out.open_appsec_exceptions = `At most ${WAF_LIMITS.exclusions} exceptions.`
  }
  for (const e of oa.exceptions) {
    const bad = exceptionProblem(e)
    if (bad) {
      out.open_appsec_exceptions = `${e.kind === "path" ? "A path" : EXCEPTION_KIND_LABEL[e.kind]} exception: ${bad}`
      break
    }
  }
  const ch = c.challenge
  if (ch.exempt_paths.length > WAF_LIMITS.paths) out.exempt_paths = `At most ${WAF_LIMITS.paths} paths.`
  if (ch.exempt_ips.length > WAF_LIMITS.ips) out.exempt_ips = `At most ${WAF_LIMITS.ips} entries.`
  return out
}

/** Split a pasted list on whitespace and commas; blanks dropped, duplicates removed in order. */
export function splitList(text: string): string[] {
  return [...new Set(text.split(/[\s,]+/).filter((s) => s !== ""))]
}

export const MODE_LABEL: Record<WafMode, string> = {
  off: "Off",
  detect: "Detection only",
  block: "Blocking",
}

/**
 * What the panel's modes are called in open-appsec's own vocabulary. The mode is
 * shared between the engines, so a site switched to open-appsec keeps its
 * `detect`/`block` value and the panel says what the agent does with it — an
 * operator reading the agent's logs must not have to guess.
 */
export const OPENAPPSEC_MODE_LABEL: Record<WafMode, string> = {
  off: "Inactive",
  detect: "detect-learn",
  block: "prevent-learn",
}

export const PARANOIA_HELP: Record<1 | 2 | 3 | 4, string> = {
  1: "Level 1: the fewest false positives. Catches common attacks. Start here.",
  2: "Level 2: more rules; some legitimate requests may be flagged.",
  3: "Level 3: strict. Expect to add exclusions for your application.",
  4: "Level 4: strictest, for sites that tolerate many false positives.",
}

export const EXCLUSION_KIND_LABEL: Record<WafExclusionKind, string> = {
  rule: "Disable rule site-wide",
  rule_path: "Disable rule for a path prefix",
  rule_arg: "Exempt a request argument from a rule",
}

/** The Unix seconds of a server timestamp, which is RFC 3339 text or already a number. */
export function toMillis(t: string | number): number {
  if (typeof t === "number") return t < 1e12 ? t * 1000 : t
  const ms = Date.parse(t)
  return Number.isNaN(ms) ? 0 : ms
}

/** Count with thousands separators, and `1.2k` above 10 000 for a tile. */
export function compactCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 10_000) return `${(n / 1000).toFixed(1)}k`
  return n.toLocaleString()
}

export function statusClass(status: number): "2xx" | "3xx" | "4xx" | "5xx" | "other" {
  if (status >= 200 && status < 300) return "2xx"
  if (status >= 300 && status < 400) return "3xx"
  if (status >= 400 && status < 500) return "4xx"
  if (status >= 500 && status < 600) return "5xx"
  return "other"
}

/** The bot-blocking switches. Each preset is a list the server keeps. */
export interface WafBotBlock {
  enabled: boolean
  empty_agent: boolean
  scanners: boolean
  ai_crawlers: boolean
  seo_crawlers: boolean
  custom: string[]
}

/** The page a blocked visitor sees, in place of nginx's bare 403. */
export interface WafErrorPage {
  enabled: boolean
  html: string
}

/** The firewall that applies to every site, on top of each site's own settings. */
export interface WafGlobal {
  bot_block: WafBotBlock
  sensitive_files: boolean
  block_trace: boolean
  ip_deny: string[]
  error_page: WafErrorPage
}

export type WafPresetName = "scanners" | "ai_crawlers" | "seo_crawlers"

export interface WafGlobalResponse {
  global: WafGlobal
  default_html: string
  presets: Record<WafPresetName, string[]>
  warnings?: string[]
}

/** The country database behind the map, and the credit its license asks for. */
export interface WafGeoStatus {
  installed: boolean
  month: string
  entries: number
  attribution: string
  attribution_url: string
  license_url: string
}

export interface WafCountry {
  code: string
  requests: number
  blocked: number
  addresses: number
}

export interface WafSourceIP {
  ip: string
  country: string
  requests: number
  errors: number
  blocked: number
}

/** Where the requests of a window came from. */
export interface WafSources {
  range: TrafficRange
  geo: WafGeoStatus
  requests: number
  blocked: number
  unlocated: number
  untracked: number
  countries: WafCountry[]
  top_ips: WafSourceIP[]
}

export const GLOBAL_LIMITS = {
  customAgents: 200,
  agentLength: 64,
  pageBytes: 64 * 1024,
} as const

/** A User-Agent substring the server accepts: letters, digits and . _ / + - only. */
export function isValidAgent(a: string): boolean {
  return /^[A-Za-z0-9._/+-]{2,64}$/.test(a)
}

/** Split a textarea into trimmed, non-empty lines. */
export function splitLines(text: string): string[] {
  return text
    .split("\n")
    .map((s) => s.trim())
    .filter((s) => s !== "")
}

const regionNames = typeof Intl !== "undefined" && "DisplayNames" in Intl ? new Intl.DisplayNames(["en"], { type: "region" }) : null

/** The English name of a two-letter country code, or the code when it has none. */
export function countryName(code: string): string {
  try {
    return regionNames?.of(code) ?? code
  } catch {
    return code
  }
}
