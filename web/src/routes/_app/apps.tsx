import { createRoute } from "@tanstack/react-router"

import { redirect } from "@tanstack/react-router"

import { appRoute } from "@/routes/_app"

/** Apps now live on the Sites page, under their type's tab. Old `/apps` links land there. */
export const appsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/apps",
  beforeLoad: () => {
    throw redirect({ to: "/sites" })
  },
})
