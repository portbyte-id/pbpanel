import type { AlertRule } from "@/lib/alertRules"
import type { Col, DisksHistory, HostHistory, ServicesHistory, SiteHistory } from "@/lib/metrics"

export const T0 = 1_700_000_000

/** `n` points, `step` seconds apart. */
export function times(n: number, step = 60): number[] {
  return Array.from({ length: n }, (_, i) => T0 + i * step)
}

/** A column of `n` copies of `v`, so a fixture says only what it varies. */
export function fill(n: number, v: number | null): Col {
  return Array.from({ length: n }, () => v)
}

export function hostHistory(n = 5, over: Partial<HostHistory> = {}): HostHistory {
  return {
    step: 60,
    t: times(n),
    cpu: fill(n, 12.5),
    cpu_max: fill(n, 30),
    load1: fill(n, 0.5),
    load1_max: fill(n, 0.9),
    load5: fill(n, 0.4),
    load15: fill(n, 0.3),
    mem_used: fill(n, 2 * 1024 ** 3),
    mem_avail: fill(n, 5 * 1024 ** 3),
    swap_used: fill(n, 0),
    net_rx: fill(n, 1000),
    net_tx: fill(n, 500),
    ...over,
  }
}

export function disksHistory(n = 5): DisksHistory {
  return {
    step: 60,
    t: times(n),
    mounts: [
      { mount: "/", used_pct: fill(n, 41), used_pct_max: fill(n, 41), used: fill(n, 1e10), rd_bps: fill(n, 100), wr_bps: fill(n, 200) },
      { mount: "/www", used_pct: fill(n, 63), used_pct_max: fill(n, 63), used: fill(n, 2e10), rd_bps: fill(n, null), wr_bps: fill(n, null) },
    ],
  }
}

export function servicesHistory(n = 5): ServicesHistory {
  return {
    step: 60,
    t: times(n),
    services: [
      { name: "panel", state: fill(n, 1), up_frac: fill(n, 1), cpu: fill(n, 3), cpu_max: fill(n, 5), rss: fill(n, 1e8), rss_max: fill(n, 1e8) },
      {
        name: "panel-php-8.4",
        state: [1, 1, 4, 4, 1],
        up_frac: [1, 1, 0, 0.5, 1],
        cpu: [2, 2, null, null, 2],
        cpu_max: fill(n, 2),
        rss: fill(n, 1e7),
        rss_max: fill(n, 1e7),
      },
    ],
  }
}

export function siteHistory(n = 5, over: Partial<SiteHistory> = {}): SiteHistory {
  return {
    step: 60,
    t: times(n),
    req: fill(n, 10),
    s2: fill(n, 8),
    s3: fill(n, 1),
    s4: fill(n, 1),
    s5: fill(n, 0),
    bytes: fill(n, 20_000),
    rt_p50: fill(n, 0.02),
    rt_p95: fill(n, 0.4),
    urt_p50: fill(n, 0.015),
    urt_p95: fill(n, 0.3),
    ...over,
  }
}

export function rule(over: Partial<AlertRule> = {}): AlertRule {
  return {
    id: 1,
    kind: "disk_pct",
    target: "*",
    threshold: 85,
    clear_threshold: 80,
    for_seconds: 300,
    clear_for_seconds: 300,
    severity: "warning",
    enabled: true,
    builtin: true,
    states: [],
    ...over,
  }
}

export const KINDS = [
  "disk_pct",
  "mem_pct",
  "swap_pct",
  "load_per_core",
  "cpu_pct",
  "service_down",
  "cert_days",
  "site_5xx_pct",
]
