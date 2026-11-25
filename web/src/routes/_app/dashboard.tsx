import { createRoute } from "@tanstack/react-router"

import { RANGES, type MetricsRange } from "@/lib/metrics"
import { DashboardPage } from "@/pages/DashboardPage"
import { appRoute } from "@/routes/_app"

export const dashboardRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/dashboard",
  // `?range=6h` is the history range of the History card.
  validateSearch: (search: Record<string, unknown>): { range?: MetricsRange } =>
    RANGES.includes(search.range as MetricsRange) ? { range: search.range as MetricsRange } : {},
  component: DashboardPage,
})
