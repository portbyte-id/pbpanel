import { createRoute } from "@tanstack/react-router"

import { appRoute } from "@/routes/_app"
import { rangeFromSearch, RANGES, type MetricsRange } from "@/lib/metrics"
import { tabFromSearch, type SitesTab } from "@/lib/sitesTabs"
import { sectionFromSearch, type SiteSection } from "@/pages/SiteDetailPage"
import { SitesPage } from "@/pages/SitesPage"

export const siteRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/sites/$domain",
  // `?waf_rule=942100&waf_path=/x` opens the Firewall section with that rule
  // ready to add as an exclusion: the WAF audit viewer's "Exclude…" button.
  // `?tab=` keeps the Sites page on the tab the site was opened from.
  // `?section=metrics` opens the site's menu on that section (alert links use it),
  // and `?range=6h` is the history range the Metrics section shows.
  validateSearch: (
    search: Record<string, unknown>,
  ): { waf_rule?: number; waf_path?: string; tab?: SitesTab; section?: SiteSection; range?: MetricsRange } => {
    const rule = Number(search.waf_rule)
    const out: { waf_rule?: number; waf_path?: string; tab?: SitesTab; section?: SiteSection; range?: MetricsRange } = {}
    const section = sectionFromSearch(search.section)
    if (section !== undefined) out.section = section
    if (RANGES.includes(search.range as MetricsRange)) out.range = rangeFromSearch(search.range)
    if (Number.isInteger(rule) && rule > 0) out.waf_rule = rule
    if (typeof search.waf_path === "string" && search.waf_path !== "") out.waf_path = search.waf_path
    if (typeof search.tab === "string") out.tab = tabFromSearch(search.tab)
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
