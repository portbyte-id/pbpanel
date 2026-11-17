/**
 * The dashboard's data: `GET /api/sysinfo` (history), `GET /api/sysinfo/stream`
 * (server-sent `snapshot` events) and `GET /api/services`. Field names are the
 * server's, see docs/design/m7-api.md.
 */

export interface LoadAverage {
  one: number
  five: number
  fifteen: number
  running: number
  total: number
}

export interface MemoryInfo {
  total_bytes: number
  available_bytes: number
  used_bytes: number
  free_bytes: number
  buffers_bytes: number
  cached_bytes: number
  swap_total_bytes: number
  swap_free_bytes: number
  swap_used_bytes: number
}

export interface DiskInfo {
  mount: string
  device: string
  fstype: string
  total_bytes: number
  used_bytes: number
  free_bytes: number
  used_percent: number
}

export interface NetInfo {
  iface: string
  rx_bytes_per_sec: number
  tx_bytes_per_sec: number
  rx_bytes: number
  tx_bytes: number
}

export interface Snapshot {
  time: string
  /** False on the first sample after the daemon starts: CPU and network rates are 0 until there are two readings. */
  rates: boolean
  cpu_percent: number
  cpu_cores: number[]
  load: LoadAverage
  memory: MemoryInfo
  uptime_seconds: number
  disks: DiskInfo[]
  net: NetInfo[]
}

export interface SysinfoResponse {
  interval_seconds: number
  latest: Snapshot | null
  history: Snapshot[]
}

export type ServiceKind = "systemd" | "container" | "app"
export type ServiceState =
  | "running"
  | "starting"
  | "stopping"
  | "stopped"
  | "failed"
  | "missing"
  | "unknown"
export type ServiceAction = "start" | "stop" | "restart"

export interface ServiceRow {
  name: string
  kind: ServiceKind
  state: ServiceState
  detail: string
  /** What the server allows on this row; empty when it cannot be controlled. */
  actions: ServiceAction[]
}

/** The server keeps one hour at five seconds; the page keeps no more than that. */
export const HISTORY_CAP = 720

/**
 * History from the last fetch plus snapshots streamed since. Keyed on `time`
 * (a snapshot can repeat once right after connecting), oldest first, newest
 * `cap` kept.
 */
export function mergeSnapshots(base: Snapshot[], extra: Snapshot[], cap = HISTORY_CAP): Snapshot[] {
  const byTime = new Map<string, Snapshot>()
  for (const s of base) byTime.set(s.time, s)
  for (const s of extra) byTime.set(s.time, s)
  const all = [...byTime.values()].sort((a, b) => Date.parse(a.time) - Date.parse(b.time))
  return all.length > cap ? all.slice(all.length - cap) : all
}

/** "12.5%". One decimal under 10, none above: a tile is read at a glance. */
export function formatPercent(value: number): string {
  if (!Number.isFinite(value)) return "–"
  return `${value < 10 ? value.toFixed(1) : Math.round(value)}%`
}

const UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"]

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "–"
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < UNITS.length - 1) {
    value /= 1024
    unit += 1
  }
  const digits = unit === 0 || value >= 100 ? 0 : value >= 10 ? 1 : 2
  return `${value.toFixed(digits)} ${UNITS[unit]}`
}

export function formatRate(bytesPerSecond: number): string {
  return `${formatBytes(bytesPerSecond)}/s`
}

/** "3d 4h", "5h 12m", "7m": the two largest units. */
export function formatUptime(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds))
  const d = Math.floor(total / 86_400)
  const h = Math.floor((total % 86_400) / 3_600)
  const m = Math.floor((total % 3_600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

/** The ratio used/total as a percentage, 0 when there is no total (no swap). */
export function ratioPercent(used: number, total: number): number {
  return total > 0 ? (used / total) * 100 : 0
}

/**
 * The `points` attribute of a sparkline. The x axis spreads the samples over
 * `width`; the y axis runs from 0 to `ceiling` (the largest value when omitted,
 * and at least 1 so a flat zero series sits on the baseline, not the top).
 */
export function sparkPoints(values: number[], width: number, height: number, ceiling?: number): string {
  if (values.length === 0) return ""
  const top = Math.max(ceiling ?? Math.max(...values), 1)
  const step = values.length > 1 ? width / (values.length - 1) : 0
  return values
    .map((v, i) => {
      const x = values.length > 1 ? i * step : width
      const y = height - (Math.min(Math.max(v, 0), top) / top) * height
      return `${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(" ")
}

export type SysinfoStreamStatus = "connecting" | "live" | "polling"

export interface SysinfoStreamHandlers {
  onSnapshot: (snapshot: Snapshot) => void
  onStatus: (status: SysinfoStreamStatus) => void
}

export interface EventSourceLike {
  addEventListener: (type: string, listener: (event: MessageEvent<string>) => void) => void
  close: () => void
}

const RECONNECT_MS = 10_000

/**
 * Follows `/api/sysinfo/stream`. Where EventSource does not exist, or after the
 * connection drops, the status is `polling` and the caller's refetch interval
 * keeps the page current; a dropped stream is retried every ten seconds.
 */
export function openSysinfoStream(
  handlers: SysinfoStreamHandlers,
  create?: (url: string) => EventSourceLike,
): { close: () => void } {
  const factory =
    create ?? (typeof EventSource === "undefined" ? null : (url: string) => new EventSource(url))
  if (factory === null) {
    handlers.onStatus("polling")
    return { close: () => undefined }
  }

  let source: EventSourceLike | null = null
  let timer: ReturnType<typeof setTimeout> | undefined
  let stopped = false

  const connect = () => {
    handlers.onStatus("connecting")
    const es = factory("/api/sysinfo/stream")
    source = es
    es.addEventListener("snapshot", (event) => {
      if (stopped) return
      try {
        handlers.onSnapshot(JSON.parse(event.data) as Snapshot)
        handlers.onStatus("live")
      } catch {
        // A malformed frame is skipped; the next sample is five seconds away.
      }
    })
    es.addEventListener("error", () => {
      if (stopped) return
      // EventSource would retry by itself but a 401 or a 503 would loop forever.
      es.close()
      handlers.onStatus("polling")
      timer = setTimeout(connect, RECONNECT_MS)
    })
  }

  connect()

  return {
    close: () => {
      stopped = true
      clearTimeout(timer)
      source?.close()
      source = null
    },
  }
}
