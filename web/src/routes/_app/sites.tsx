import { createRoute } from "@tanstack/react-router"

import { SitesPage } from "@/pages/SitesPage"
import { appRoute } from "@/routes/_app"

export const sitesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/sites",
  component: SitesPage,
})
