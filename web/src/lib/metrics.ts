/**
 * The metrics history client (docs/API.md "Metrics history"): typed calls, the
 * react-query hooks the dashboard and the site menu share, and the pure helpers
 * they need. Every call goes through the shared ApiClient, so the CSRF token,
 * the 401 redirect and the error envelope apply.
 *
 * Every chart response is columnar: `t` is the start of each point and each
 * series is an array of the same length. A point with no data is `null`.
 */

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useNavigate, useSearch } from "@tanstack/react-router"

import { api, ApiError, type ApiClient } from "@/lib/api"
import { pollWhileVisible } from "@/lib/wafQueries"

export const RANGES = ["1h", "6h", "24h", "7d", "30d", "90d"] as const
export type MetricsRange = (typeof RANGES)[number]
export const DEFAULT_RANGE: MetricsRange = "1h"

/** Spoken names, for the picker's radios. */
export const RANGE_NAMES: Record<MetricsRange, string> = {
  "1h": "1 hour",
  "6h": "6 hours",
  "24h": "24 hours",
  "7d": "7 days",
  "30d": "30 days",
  "90d": "90 days",
}

/** The `range` search value, or the default when it is missing or unknown. */
export function rangeFromSearch(value: unknown): MetricsRange {
  return RANGES.includes(value as MetricsRange) ? (value as MetricsRange) : DEFAULT_RANGE
}

/** The picker's value, kept in the URL (`?range=6h`) so a reload and a shared link keep it. */
export function useRangeParam(): [MetricsRange, (next: MetricsRange) => void] {
  const search = useSearch({ strict: false }) as { range?: unknown }
  const navigate = useNavigate()
  const range = rangeFromSearch(search.range)
  const set = (next: MetricsRange) => {
    void navigate({
      search: ((prev: Record<string, unknown>) => ({ ...prev, range: next })) as never,
      replace: true,
    })
  }
  return [range, set]
}

/**
 * A custom window: two local calendar dates (`YYYY-MM-DD`), both days included.
 * The daemon takes unix seconds, so the dates become midnights in the browser's
 * zone when the request is made; `to` is capped at now.
 */
export interface CustomSpan {
  from: string
  to: string
}

/** What a history chart asks for: a preset ending now, or a custom window. */
export type HistorySpan = MetricsRange | CustomSpan

/** The longest window the daemon answers; it refuses anything wider with 400. */
export const MAX_CUSTOM_DAYS = 400

const DAY_MS = 86_400_000
const DATE_RE = /^\d{4}-\d{2}-\d{2}$/

/** True for a real calendar date in `YYYY-MM-DD` form (not 2026-02-31). */
export function isDateString(value: unknown): value is string {
  if (typeof value !== "string" || !DATE_RE.test(value)) return false
  const [y, m, d] = value.split("-").map(Number)
  const date = new Date(y, m - 1, d)
  return date.getFullYear() === y && date.getMonth() === m - 1 && date.getDate() === d
}

/** The local calendar date of `date`, as `YYYY-MM-DD`. */
export function localDateString(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

/** Local midnight at the start of `date`, moved by `plusDays`. */
function localMidnight(date: string, plusDays = 0): Date {
  const [y, m, d] = date.split("-").map(Number)
  return new Date(y, m - 1, d + plusDays)
}

/** Why a custom window cannot be asked for, in words for the reader; null when it can. */
export function customProblem(span: CustomSpan, now: Date = new Date()): string | null {
  if (!isDateString(span.from) || !isDateString(span.to)) return "Pick a start date and an end date."
  if (span.from > span.to) return "The start date is after the end date."
  if (span.from > localDateString(now)) return "The start date is in the future."
  const days = (localMidnight(span.to, 1).getTime() - localMidnight(span.from).getTime()) / DAY_MS
  if (days > MAX_CUSTOM_DAYS) return `Pick a window of ${MAX_CUSTOM_DAYS} days or fewer.`
  return null
}

/** The query string that asks the daemon for a span: `range=6h`, or `from=&to=` in unix seconds. */
export function spanQuery(span: HistorySpan, nowMs: number = Date.now()): string {
  if (typeof span === "string") return `range=${encodeURIComponent(span)}`
  const from = Math.floor(localMidnight(span.from).getTime() / 1000)
  const end = Math.floor(localMidnight(span.to, 1).getTime() / 1000)
  const to = Math.min(end, Math.floor(nowMs / 1000))
  return `from=${from}&to=${to}`
}

/** A stable string for a span, for react-query keys: the same window keeps its cache entry. */
export function spanKey(span: HistorySpan): string {
  return typeof span === "string" ? span : `${span.from}..${span.to}`
}

/** The window's name for a card's note: "6 hours", or "2026-11-01 to 2026-11-10". */
export function spanName(span: HistorySpan): string {
  if (typeof span === "string") return RANGE_NAMES[span]
  return span.from === span.to ? span.from : `${span.from} to ${span.to}`
}

/**
 * The span the URL asks for: `?from=&to=` when both are dates, otherwise the
 * preset from `?range=`. Setting one drops the other, so a link has one window.
 */
export function useSpanParam(): [
  HistorySpan,
  { preset: (next: MetricsRange) => void; custom: (next: CustomSpan) => void },
] {
  const search = useSearch({ strict: false }) as { range?: unknown; from?: unknown; to?: unknown }
  const navigate = useNavigate()
  const span: HistorySpan =
    isDateString(search.from) && isDateString(search.to)
      ? { from: search.from, to: search.to }
      : rangeFromSearch(search.range)
  const write = (patch: Record<string, unknown>, drop: string[]) => {
    void navigate({
      search: ((prev: Record<string, unknown>) => {
        const next = { ...prev }
        for (const key of drop) delete next[key]
        return { ...next, ...patch }
      }) as never,
      replace: true,
    })
  }
  return [
    span,
    {
      preset: (next) => write({ range: next }, ["from", "to"]),
      custom: (next) => write({ from: next.from, to: next.to }, ["range"]),
    },
  ]
}

export type Col = (number | null)[]

export interface HostHistory {
  step: number
  t: number[]
  cpu: Col
  cpu_max: Col
  load1: Col
  load1_max: Col
  load5: Col
  load15: Col
  mem_used: Col
  mem_avail: Col
  swap_used: Col
  net_rx: Col
  net_tx: Col
}

export interface MountHistory {
  mount: string
  used_pct: Col
  used_pct_max: Col
  used: Col
  rd_bps: Col
  wr_bps: Col
}

export interface DisksHistory {
  step: number
  t: number[]
  mounts: MountHistory[]
}

export interface ServiceHistory {
  name: string
  state: Col
  up_frac: Col
  cpu: Col
  cpu_max: Col
  rss: Col
  rss_max: Col
}

export interface ServicesHistory {
  step: number
  t: number[]
  services: ServiceHistory[]
}

export interface SiteHistory {
  step: number
  t: number[]
  req: Col
  s2: Col
  s3: Col
  s4: Col
  s5: Col
  bytes: Col
  rt_p50: Col
  rt_p95: Col
  urt_p50: Col
  urt_p95: Col
}

export interface TierStatus {
  table: string
  rows: number
  oldest: number
}

export interface MetricsStatus {
  enabled: boolean
  file_bytes: number
  wal_bytes: number
  tiers: TierStatus[]
  last_flush: number
  last_error: string
  last_error_at: number
  dropped: number
}

function path(kind: string, span: HistorySpan): string {
  return `/api/metrics/${kind}?${spanQuery(span)}`
}

export function hostHistory(span: HistorySpan, client: ApiClient = api): Promise<HostHistory> {
  return client.request<HostHistory>(path("host", span))
}

export function disksHistory(span: HistorySpan, client: ApiClient = api): Promise<DisksHistory> {
  return client.request<DisksHistory>(path("disks", span))
}

export function servicesHistory(range: MetricsRange, client: ApiClient = api): Promise<ServicesHistory> {
  return client.request<ServicesHistory>(path("services", range))
}

export function siteHistory(domain: string, range: MetricsRange, client: ApiClient = api): Promise<SiteHistory> {
  return client.request<SiteHistory>(path(`sites/${encodeURIComponent(domain)}`, range))
}

export function metricsStatus(client: ApiClient = api): Promise<MetricsStatus> {
  return client.request<MetricsStatus>("/api/metrics/status")
}

export function clearMetrics(client: ApiClient = api): Promise<void> {
  return client.request<void>("/api/metrics/clear", { method: "POST" })
}

export const metricsKeys = {
  all: ["metrics"] as const,
  host: (span: HistorySpan) => ["metrics", "host", spanKey(span)] as const,
  disks: (span: HistorySpan) => ["metrics", "disks", spanKey(span)] as const,
  services: (range: MetricsRange) => ["metrics", "services", range] as const,
  site: (domain: string, range: MetricsRange) => ["metrics", "sites", domain, range] as const,
  status: ["metrics", "status"] as const,
}

/** New points land once a minute, so asking more often than this only repeats the answer. */
export const HISTORY_POLL_MS = 30_000

/**
 * The shared options: no retry (a 409 or a 503 is a state to explain, not a blip),
 * the previous range stays on screen while the next one loads, and the poll
 * stops while the tab is hidden.
 */
const historyOptions = {
  retry: false,
  placeholderData: keepPreviousData,
  refetchInterval: pollWhileVisible(HISTORY_POLL_MS),
} as const

/** `enabled: false` keeps a query idle, for a window that cannot be asked for. */
export function useHostHistory(span: HistorySpan, enabled = true) {
  return useQuery({
    queryKey: metricsKeys.host(span),
    queryFn: () => hostHistory(span),
    enabled,
    ...historyOptions,
  })
}

export function useDisksHistory(span: HistorySpan, enabled = true) {
  return useQuery({
    queryKey: metricsKeys.disks(span),
    queryFn: () => disksHistory(span),
    enabled,
    ...historyOptions,
  })
}

export function useServicesHistory(range: MetricsRange) {
  return useQuery({
    queryKey: metricsKeys.services(range),
    queryFn: () => servicesHistory(range),
    ...historyOptions,
  })
}

export function useSiteHistory(domain: string, range: MetricsRange) {
  return useQuery({
    queryKey: metricsKeys.site(domain, range),
    queryFn: () => siteHistory(domain, range),
    ...historyOptions,
  })
}

export function useMetricsStatus() {
  return useQuery({
    queryKey: metricsKeys.status,
    queryFn: () => metricsStatus(),
    retry: false,
    refetchInterval: pollWhileVisible(HISTORY_POLL_MS),
  })
}

/** Deletes every stored chart. Alert rules are not touched. */
export function useClearMetrics() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "History cleared" },
    mutationFn: () => clearMetrics(),
    onSuccess: () => client.invalidateQueries({ queryKey: metricsKeys.all }),
  })
}

/* --- States and wording ---------------------------------------------------- */

export type HistoryState =
  | { kind: "loading" }
  /** 409: `metrics.enabled` is off. */
  | { kind: "disabled"; detail: string }
  /** 503: the history database did not open. */
  | { kind: "unavailable"; detail: string }
  | { kind: "error"; detail: string }
  | { kind: "ready" }

/** What a history query's failure means to the reader. */
export function classifyHistory(q: { isPending: boolean; error: unknown }): HistoryState {
  if (q.error instanceof ApiError) {
    if (q.error.status === 409) return { kind: "disabled", detail: q.error.detail }
    if (q.error.status === 503) return { kind: "unavailable", detail: q.error.detail }
    return { kind: "error", detail: q.error.detail }
  }
  if (q.error) return { kind: "error", detail: "The history could not be loaded." }
  if (q.isPending) return { kind: "loading" }
  return { kind: "ready" }
}

/** True when no series has a single value: nothing has been recorded for this range yet. */
export function hasNoData(...series: Col[]): boolean {
  return series.every((s) => s.every((v) => v === null || !Number.isFinite(v)))
}

export const PERCENTILE_NOTE =
  "p50 and p95 are estimates from fixed histogram bins, good to about 20%."

/** The tooltip for a percentile series: what it means, and how far to trust it. */
export function percentileTooltip(name: "p50" | "p95", what = "requests"): string {
  const share = name === "p50" ? "half" : "95%"
  return `${name}: ${share} of ${what} were faster than this. ${PERCENTILE_NOTE}`
}
