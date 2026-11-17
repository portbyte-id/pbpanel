import { createRoute } from "@tanstack/react-router"

import { RuntimesPage } from "@/pages/RuntimesPage"
import { appRoute } from "@/routes/_app"

export const runtimesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/runtimes",
  component: RuntimesPage,
})
