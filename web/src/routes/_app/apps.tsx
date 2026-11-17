import { createRoute } from "@tanstack/react-router"

import { AppsPage } from "@/pages/AppsPage"
import { appRoute } from "@/routes/_app"

export const appsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/apps",
  component: AppsPage,
})
