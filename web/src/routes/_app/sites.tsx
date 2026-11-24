import { createRoute } from "@tanstack/react-router"

import { appRoute } from "@/routes/_app"
import { tabFromSearch, type SitesTab } from "@/lib/sitesTabs"
import { SitesPage } from "@/pages/SitesPage"

export const sitesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/sites",
  // `?proxy=<url>` opens the add-site dialog on "Another server" with that
  // address filled in: an app's "Add domain" shortcut. `?tab=` picks the tab.
  validateSearch: (search: Record<string, unknown>): { proxy?: string; tab?: SitesTab } => {
    const out: { proxy?: string; tab?: SitesTab } = {}
    if (typeof search.proxy === "string" && search.proxy !== "") out.proxy = search.proxy
    if (typeof search.tab === "string") out.tab = tabFromSearch(search.tab)
    return out
  },
  component: SitesPage,
})
