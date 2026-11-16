import { createRoute, redirect } from "@tanstack/react-router"

import { appRoute } from "@/routes/_app"

/**
 * `/` has no page of its own: it sends the operator to the first built section.
 * It is a redirect rather than a dashboard because the dashboard is M5, and a
 * placeholder dashboard would be a screen that looks finished and is not.
 */
export const appIndexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/settings", replace: true })
  },
})
