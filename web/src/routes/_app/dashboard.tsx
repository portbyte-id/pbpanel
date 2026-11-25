import { createRoute } from "@tanstack/react-router"

import { DashboardPage } from "@/pages/DashboardPage"
import { appRoute } from "@/routes/_app"

export const dashboardRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/dashboard",
  component: DashboardPage,
})
