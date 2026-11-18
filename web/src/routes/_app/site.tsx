import { createRoute } from "@tanstack/react-router"

import { SitesPage } from "@/pages/SitesPage"
import { appRoute } from "@/routes/_app"

export const siteRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/sites/$domain",
  // `?waf_rule=942100&waf_path=/x` opens the Firewall section with that rule
  // ready to add as an exclusion: the WAF audit viewer's "Exclude…" button.
  validateSearch: (search: Record<string, unknown>): { waf_rule?: number; waf_path?: string } => {
    const rule = Number(search.waf_rule)
    const out: { waf_rule?: number; waf_path?: string } = {}
    if (Number.isInteger(rule) && rule > 0) out.waf_rule = rule
    if (typeof search.waf_path === "string" && search.waf_path !== "") out.waf_path = search.waf_path
    return out
  },
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
