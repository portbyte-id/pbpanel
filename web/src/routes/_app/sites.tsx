import { createRoute } from "@tanstack/react-router"

import { SitesPage } from "@/pages/SitesPage"
import { appRoute } from "@/routes/_app"

export const sitesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/sites",
  // `?proxy=<url>` opens the add-site dialog on "Another server" with that
  // address filled in: the Apps page's "Proxy site" shortcut (development plan 5.6).
  validateSearch: (search: Record<string, unknown>): { proxy?: string } =>
    typeof search.proxy === "string" && search.proxy !== "" ? { proxy: search.proxy } : {},
  component: SitesPage,
})
