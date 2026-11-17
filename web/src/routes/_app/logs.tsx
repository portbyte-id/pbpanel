import { createRoute } from "@tanstack/react-router"

import { LogsPage } from "@/pages/LogsPage"
import { appRoute } from "@/routes/_app"

export const logsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/logs",
  component: LogsPage,
})
