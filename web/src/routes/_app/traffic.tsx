import { createRoute, redirect } from "@tanstack/react-router"

import { appRoute } from "@/routes/_app"

/** The Traffic page is part of the WAF page now; old links land there. */
export const trafficRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/traffic",
  beforeLoad: () => {
    throw redirect({ to: "/waf" })
  },
})
