import type { AppType, PanelApp, Site } from "@/lib/api"

/**
 * The tabs of the Sites page. "web" holds PHP sites and reverse proxies; each
 * other tab holds the apps of one type and the sites that pass requests to them.
 */
export type SitesTab = "web" | "node" | "go" | "binary"

export const sitesTabs: { value: SitesTab; label: string }[] = [
  { value: "web", label: "PHP & Proxy" },
  { value: "node", label: "Node.js" },
  { value: "go", label: "Go" },
  { value: "binary", label: "Binary" },
]

/** The `tab` search value, or "web" when it is missing or unknown. */
export function tabFromSearch(value: unknown): SitesTab {
  return sitesTabs.some((t) => t.value === value) ? (value as SitesTab) : "web"
}

/** The tab an app belongs to: the one for its type. */
export function tabForApp(type: AppType): SitesTab {
  if (type === "node") return "node"
  return type === "go" ? "go" : "binary"
}

/**
 * The tab a site belongs to. A site with a subpath route to an app of this panel
 * belongs to that app's tab and nowhere else; the first such route decides when
 * there are several. Every other site (PHP, static, or an outside server) is on
 * "web".
 */
export function tabOfSite(site: Site, apps: PanelApp[]): SitesTab {
  for (const route of site.proxy_routes ?? []) {
    const app = apps.find((a) => a.proxy_url !== "" && a.proxy_url === route.target)
    if (app) return tabForApp(app.type)
  }
  return "web"
}
