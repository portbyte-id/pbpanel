import { createRoute, redirect } from "@tanstack/react-router"

import { appRoute } from "@/routes/_app"

/** `/` has no page of its own: it sends the operator to the dashboard, the first section. */
export const appIndexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/dashboard", replace: true })
  },
})
