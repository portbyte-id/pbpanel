/**
 * The firewall (WAF) contract: per-site protection settings, the traffic monitor
 * and the ModSecurity audit events. Types mirror the server's JSON; the helpers
 * hold the client-side limits so a bad value is caught before a config apply.
 */

export type WafMode = "off" | "detect" | "block"
export type WafExclusionKind = "rule" | "rule_path" | "rule_arg"

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

export interface WafConfig {
  mode: WafMode
  paranoia: 1 | 2 | 3 | 4
  rate_limit: WafRateLimit
  path_blocklist: string[]
  ip_rules: { allow: string[]; deny: string[] }
  exclusions: WafExclusion[]
}

export interface WafSiteStatus {
  available: boolean
  flavor: "pqc" | "standard" | "unknown"
  reason: string
  attached: boolean
  crs_version: string
}

export interface WafSiteResponse {
  config: WafConfig
  status: WafSiteStatus
  warnings?: string[]
}

export interface WafOverviewSite {
  domain: string
  mode: WafMode
  paranoia: number
  rate_limit: boolean
  path_blocklist: number
  ip_rules: number
  exclusions: number
}

export interface WafOverview {
  available: boolean
  flavor: string
  reason: string
  sites: WafOverviewSite[]
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
  mode: "off",
  paranoia: 1,
  rate_limit: { enabled: false, requests_per_second: 10, burst: 20 },
  path_blocklist: [],
  ip_rules: { allow: [], deny: [] },
  exclusions: [],
}

/** Server limits, mirrored so the form can refuse before a request. */
export const WAF_LIMITS = {
  paths: 200,
  ips: 500,
  exclusions: 200,
  note: 200,
  maxRuleId: 9_999_999,
} as const

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
  return n.toLocaleString("en-US")
}

export function statusClass(status: number): "2xx" | "3xx" | "4xx" | "5xx" | "other" {
  if (status >= 200 && status < 300) return "2xx"
  if (status >= 300 && status < 400) return "3xx"
  if (status >= 400 && status < 500) return "4xx"
  if (status >= 500 && status < 600) return "5xx"
  return "other"
}
