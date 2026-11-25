import { createRoute } from "@tanstack/react-router"

import { isDateString, RANGES, type MetricsRange } from "@/lib/metrics"
import { MonitoringPage } from "@/pages/MonitoringPage"
import { appRoute } from "@/routes/_app"

/** `?range=6h` for a preset, or `?from=2026-11-01&to=2026-11-10` for a custom window. */
export interface MonitoringSearch {
  range?: MetricsRange
  from?: string
  to?: string
}

export const monitoringRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/monitoring",
  validateSearch: (search: Record<string, unknown>): MonitoringSearch => {
    const out: MonitoringSearch = {}
    if (RANGES.includes(search.range as MetricsRange)) out.range = search.range as MetricsRange
    if (isDateString(search.from)) out.from = search.from
    if (isDateString(search.to)) out.to = search.to
    return out
  },
  component: MonitoringPage,
})
