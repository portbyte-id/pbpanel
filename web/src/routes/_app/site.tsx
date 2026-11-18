import { createRoute } from "@tanstack/react-router"

import { SitesPage } from "@/pages/SitesPage"
import { appRoute } from "@/routes/_app"

export const siteRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/sites/$domain",
  component: SiteRoute,
})

/**
 * The page takes the domain as a prop rather than reading the route itself, so
 * the page does not import the route that imports it. Keyed on the domain so
 * moving between two sites starts each with its own state.
 */
function SiteRoute() {
  const { domain } = siteRoute.useParams()
  return <SitesPage openDomain={domain} />
}
