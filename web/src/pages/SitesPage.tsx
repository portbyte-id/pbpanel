import { SkeletonRows } from "@/components/ui/skeleton"
import { Link, useNavigate, useSearch } from "@tanstack/react-router"
import { Plus } from "lucide-react"
import { useMemo, useState } from "react"

import { CreateSiteDialog } from "@/components/CreateSiteDialog"
import { ErrorState } from "@/components/ErrorState"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { TableFilter, TablePagination, TableToolbar, usePagedRows } from "@/components/ui/table-controls"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { TLSCell } from "@/components/TLSCell"
import { type AppType, type Site } from "@/lib/api"
import { matchesQuery } from "@/lib/table"
import { useApps, useSites, useTLSCerts } from "@/lib/queries"
import { sitesTabs, tabFromSearch, tabOfSite, type SitesTab } from "@/lib/sitesTabs"
import { AppsTable } from "@/pages/AppsPage"
import { SiteDetailDialog } from "@/pages/SiteDetailPage"

export function kindLabel(kind: Site["kind"]): string {
  if (kind === "php") return "PHP"
  return kind === "proxy" ? "Reverse proxy" : "Static files"
}

/**
 * What the site actually does, from its parts rather than its kind: a site may
 * serve files, run PHP and proxy subpaths at once. The kind badge stays as the
 * primary mode; this is the detail line the list shows beside it.
 */
export function describeSite(site: Site): string[] {
  const parts: string[] = []
  if (site.php_version) parts.push(`PHP ${site.php_version}`)
  if (site.root) parts.push("files")
  for (const r of site.proxy_routes ?? []) {
    parts.push(`${r.path} → ${r.target.replace(/^https?:\/\//, "")}`)
  }
  return parts
}

/** What each app tab says about its kind of app, and the type it lists. */
const appTabs: Record<Exclude<SitesTab, "web">, { type: AppType; intro: string; empty: string }> = {
  node: {
    type: "node",
    intro: "Node.js apps run under supervisord on the Node version you pick. They keep running when the panel restarts, and start again if they exit.",
    empty: "No Node.js apps yet. Add one with its entry script.",
  },
  go: {
    type: "go",
    intro: "Compiled Go programs run under supervisord. They keep running when the panel restarts, and start again if they exit.",
    empty: "No Go programs yet. Add one with the path to its binary.",
  },
  binary: {
    type: "command",
    intro: "Other programs, given by their absolute path, run under supervisord. They keep running when the panel restarts, and start again if they exit.",
    empty: "No other programs yet. Add one with the path to its binary.",
  },
}

/**
 * The Sites page: one tab for PHP and reverse proxies, and one tab per app type.
 * A site that passes a subpath to an app of this panel is listed under that app's
 * tab only. The tab is kept in the URL, so a detail or a reload returns to it.
 */
export function SitesPage({ openDomain }: { openDomain?: string } = {}) {
  const sites = useSites()
  const apps = useApps()
  // Certificates are a nicety here: if they fail to load the list still works.
  const certs = useTLSCerts()
  // `?proxy=<url>` comes from an app's "Add domain" button: the dialog opens on
  // "Another server" with the app's address filled in.
  const { proxy, tab: tabParam, waf_rule, waf_path } = useSearch({ strict: false }) as {
    proxy?: string
    tab?: unknown
    waf_rule?: number
    waf_path?: string
  }
  const tab = tabFromSearch(tabParam)
  const current = sitesTabs.find((t) => t.value === tab) ?? sitesTabs[0]
  const navigate = useNavigate()
  const [creating, setCreating] = useState(proxy !== undefined)
  const [filter, setFilter] = useState("")
  const [kindFilter, setKindFilter] = useState("all")

  const siteList = useMemo(() => sites.data ?? [], [sites.data])
  const appList = useMemo(() => apps.data ?? [], [apps.data])
  // Only used to name the app behind a proxy site; the list works without it.
  const appByURL = useMemo(
    () => new Map(appList.filter((a) => a.proxy_url !== "").map((a) => [a.proxy_url, a.name])),
    [appList],
  )
  const certByDomain = useMemo(() => new Map((certs.data ?? []).map((c) => [c.domain, c])), [certs.data])

  const tabSites = useMemo(() => siteList.filter((s) => tabOfSite(s, appList) === tab), [siteList, appList, tab])
  const visible = useMemo(
    () =>
      tabSites.filter(
        (s) =>
          (kindFilter === "all" || s.kind === kindFilter) &&
          matchesQuery(
            [s.domain, ...s.aliases, s.root, ...(s.proxy_routes ?? []).flatMap((r) => [r.path, r.target])],
            filter,
          ),
      ),
    [tabSites, filter, kindFilter],
  )
  const pager = usePagedRows(visible, `${tab}|${filter}|${kindFilter}`)

  const selectTab = (next: string) => {
    void navigate({ to: "/sites", search: { tab: tabFromSearch(next) }, replace: true })
  }

  const toolbar = (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <p className="text-muted-foreground text-sm">
        {tab === "web"
          ? tabSites.length === 0
            ? "No PHP or proxy sites yet."
            : `${tabSites.length} site${tabSites.length === 1 ? "" : "s"} served by nginx.`
          : tabSites.length === 0
            ? `No site passes requests to a ${current.label} app yet.`
            : `${tabSites.length} site${tabSites.length === 1 ? "" : "s"} pass requests to ${current.label} apps.`}
      </p>
      <div className="flex items-center gap-2">
        {tab === "web" && (
          <Button onClick={() => setCreating(true)}>
            <Plus aria-hidden="true" />
            Add site
          </Button>
        )}
      </div>
    </div>
  )

  const siteTable = sites.isPending ? (
    <SkeletonRows label="Loading sites…" />
  ) : sites.error ? (
    <ErrorState error={sites.error} fallback="The sites could not be loaded." onRetry={() => void sites.refetch()} />
  ) : tabSites.length === 0 && tab === "web" ? (
    <div className="rounded-lg border border-dashed p-8 text-center">
      <p className="text-sm font-medium">Add your first site</p>
      <p className="text-muted-foreground mx-auto mt-1 max-w-md text-sm">
        A site answers for one or more domains. It can serve files from a folder, run PHP, and
        pass subpaths such as <code translate="no">/api</code> to apps on this server — all at once.
      </p>
      <Button className="mt-4" onClick={() => setCreating(true)}>
        <Plus aria-hidden="true" />
        Add site
      </Button>
    </div>
  ) : tabSites.length === 0 ? (
    <p className="text-muted-foreground border-border rounded-md border border-dashed p-6 text-sm">
      Use <span className="font-medium">Add domain</span> on an app above to put a site in front of it.
    </p>
  ) : (
    <div className="flex flex-col gap-3">
      <TableToolbar search={filter} onSearchChange={setFilter} searchLabel="Filter sites" placeholder="Filter sites…">
        <TableFilter
          label="Type"
          value={kindFilter}
          onChange={setKindFilter}
          options={[
            { value: "php", label: "PHP" },
            { value: "proxy", label: "Reverse proxy" },
            { value: "static", label: "Static files" },
          ]}
        />
      </TableToolbar>
      {pager.total === 0 ? (
        <p className="text-muted-foreground text-sm" role="status">
          No rows match the filters.
        </p>
      ) : (
        <>
    <div className="overflow-x-auto rounded-lg border">
      <table className="w-full text-sm">
        <caption className="sr-only">Sites served by nginx</caption>
        <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
          <tr>
            <th scope="col" className="px-3 py-1.5 font-medium">Domain</th>
            <th scope="col" className="px-3 py-1.5 font-medium">Type</th>
            <th scope="col" className="px-3 py-1.5 font-medium">HTTPS</th>
            <th scope="col" className="px-3 py-1.5 font-medium">Serves</th>
            <th scope="col" className="px-3 py-1.5 font-medium">Other domains</th>
          </tr>
        </thead>
        <tbody className="divide-border divide-y">
          {pager.rows.map((site) => (
            <tr
              key={site.domain}
              className="hover:bg-accent/40 cursor-pointer"
              // The domain link is the keyboard and screen-reader path; the
              // row click is a larger target for mouse and touch.
              onClick={(e) => {
                if ((e.target as HTMLElement).closest("a")) return
                void navigate({ to: "/sites/$domain", params: { domain: site.domain }, search: { tab } })
              }}
            >
              <td className="px-3 py-1.5 pointer-coarse:py-3">
                <Link
                  to="/sites/$domain"
                  params={{ domain: site.domain }}
                  search={{ tab }}
                  className="font-medium underline-offset-4 hover:underline"
                >
                  {site.domain}
                </Link>
              </td>
              <td className="px-3 py-1.5">
                <Badge variant="secondary">{kindLabel(site.kind)}</Badge>
              </td>
              <td className="px-3 py-1.5 whitespace-nowrap">
                {certs.isPending ? (
                  <span className="text-muted-foreground">…</span>
                ) : (
                  <TLSCell cert={certByDomain.get(site.domain)} />
                )}
              </td>
              <td className="px-3 py-1.5">
                {describeSite(site).length === 0 ? (
                  <span className="text-muted-foreground">—</span>
                ) : (
                  <ul className="flex flex-col gap-0.5 font-mono text-xs">
                    {describeSite(site).map((part) => (
                      <li key={part} className="break-all">
                        {part}
                      </li>
                    ))}
                    {(site.proxy_routes ?? [])
                      .filter((r) => appByURL.has(r.target))
                      .map((r) => (
                        <li key={r.path + "-app"} className="text-muted-foreground font-sans">
                          {r.path}: app {appByURL.get(r.target)}
                        </li>
                      ))}
                  </ul>
                )}
              </td>
              <td className="text-muted-foreground px-3 py-1.5">
                {site.aliases.length === 0 ? "—" : site.aliases.join(", ")}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
    <TablePagination pager={pager} />
        </>
      )}
    </div>
  )

  const appTab = tab === "web" ? null : appTabs[tab]
  const appSection =
    appTab === null ? null : apps.isPending ? (
      <SkeletonRows label={`Loading ${current.label} apps…`} />
    ) : apps.error ? (
      <ErrorState error={apps.error} fallback="The apps could not be loaded." onRetry={() => void apps.refetch()} />
    ) : (
      <AppsTable
        type={appTab.type}
        apps={appList}
        sites={siteList}
        certs={certByDomain}
        certsPending={certs.isPending}
        intro={appTab.intro}
        empty={appTab.empty}
      />
    )

  return (
    <div className="flex flex-col gap-4">
      <Tabs value={tab} onValueChange={selectTab}>
        <TabsList aria-label="Sites and apps by type" className="overflow-x-auto">
          {sitesTabs.map((t) => (
            <TabsTrigger key={t.value} value={t.value}>
              {t.label}
            </TabsTrigger>
          ))}
        </TabsList>

        <TabsContent value={tab} className="flex flex-col gap-4">
          {appTab === null ? (
            <>
              {toolbar}
              {siteTable}
            </>
          ) : (
            appSection
          )}
        </TabsContent>
      </Tabs>

      <CreateSiteDialog
        open={creating}
        initial={proxy === undefined ? undefined : { target: proxy }}
        onOpenChange={(open) => {
          setCreating(open)
          // Drop the parameter so a reload does not reopen a dialog that was closed.
          if (!open && proxy !== undefined) void navigate({ to: "/sites", search: { tab }, replace: true })
        }}
        onCreated={(domain) => void navigate({ to: "/sites/$domain", params: { domain }, search: { tab } })}
      />

      {openDomain !== undefined && (
        <SiteDetailDialog
          key={openDomain}
          domain={openDomain}
          wafPrefill={waf_rule === undefined ? undefined : { rule: waf_rule, path: waf_path }}
          onClose={() => void navigate({ to: "/sites", search: { tab } })}
        />
      )}
    </div>
  )
}
