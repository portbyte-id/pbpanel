import { SkeletonRows } from "@/components/ui/skeleton"
import { Link, useNavigate, useSearch } from "@tanstack/react-router"
import { Plus, Search } from "lucide-react"
import { useMemo, useState } from "react"

import { CreateSiteDialog } from "@/components/CreateSiteDialog"
import { ErrorState } from "@/components/ErrorState"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { type CertView, type Site } from "@/lib/api"
import { SiteDetailDialog } from "@/pages/SiteDetailPage"
import { useApps, useSites, useTLSCerts } from "@/lib/queries"

export function kindLabel(kind: Site["kind"]): string {
  if (kind === "php") return "PHP"
  return kind === "proxy" ? "Reverse proxy" : "Static files"
}

/** HTTPS state for the list: expiry soon is a warning, not just a number. */
function TLSCell({ cert }: { cert: CertView | undefined }) {
  if (!cert) return <span className="text-muted-foreground">No HTTPS</span>
  if (cert.days_left < 0) return <Badge variant="destructive">Expired</Badge>
  if (cert.days_left <= 14) return <Badge variant="warning">Expires in {cert.days_left}d</Badge>
  return <Badge variant="success">HTTPS · {cert.days_left}d</Badge>
}

/** The sites list. A row leads to the site; the button adds one. */
export function SitesPage({ openDomain }: { openDomain?: string } = {}) {
  const { data, isPending, error, refetch } = useSites()
  // Certificates are a nicety here: if they fail to load the list still works.
  const certs = useTLSCerts()
  // Only used to name the app behind a proxy site; the list works without it.
  const apps = useApps().data
  const appByURL = useMemo(
    () => new Map((apps ?? []).filter((a) => a.proxy_url !== "").map((a) => [a.proxy_url, a.name])),
    [apps],
  )
  // `?proxy=<url>` comes from an app's "Proxy site" button: the dialog opens on
  // "Another server" with the app's address filled in.
  const { proxy, waf_rule, waf_path } = useSearch({ strict: false }) as {
    proxy?: string
    waf_rule?: number
    waf_path?: string
  }
  const [creating, setCreating] = useState(proxy !== undefined)
  const [filter, setFilter] = useState("")
  const navigate = useNavigate()

  const certByDomain = useMemo(() => new Map((certs.data ?? []).map((c) => [c.domain, c])), [certs.data])

  const sites = useMemo(() => data ?? [], [data])
  const visible = useMemo(() => {
    const q = filter.trim().toLowerCase()
    if (q === "") return sites
    return sites.filter((s) =>
      `${s.domain} ${s.aliases.join(" ")} ${s.proxy_target ?? ""} ${s.root ?? ""}`.toLowerCase().includes(q),
    )
  }, [sites, filter])

  if (isPending) {
    return <SkeletonRows label="Loading sites…" />
  }
  if (error) {
    return <ErrorState error={error} fallback="The sites could not be loaded." onRetry={() => void refetch()} />
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-muted-foreground text-sm">
          {sites.length === 0
            ? "No sites yet."
            : `${sites.length} site${sites.length === 1 ? "" : "s"} served by nginx.`}
        </p>
        <div className="flex items-center gap-2">
          {sites.length > 5 && (
            <div className="relative">
              <Search
                className="text-muted-foreground pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2"
                aria-hidden="true"
              />
              <Input
                type="search"
                aria-label="Filter sites"
                placeholder="Filter sites"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                className="w-48 pl-7"
              />
            </div>
          )}
          <Button onClick={() => setCreating(true)}>
            <Plus aria-hidden="true" />
            Add site
          </Button>
        </div>
      </div>

      {sites.length === 0 ? (
        <div className="rounded-lg border border-dashed p-8 text-center">
          <p className="text-sm font-medium">Add your first site</p>
          <p className="text-muted-foreground mx-auto mt-1 max-w-md text-sm">
            A site answers for one or more domains. It can serve files from a folder or pass
            requests to an app running on this server.
          </p>
          <Button className="mt-4" onClick={() => setCreating(true)}>
            <Plus aria-hidden="true" />
            Add site
          </Button>
        </div>
      ) : visible.length === 0 ? (
        <p className="text-muted-foreground text-sm" role="status">
          No site matches “{filter}”.
        </p>
      ) : (
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
              {visible.map((site) => (
                <tr
                  key={site.domain}
                  className="hover:bg-accent/40 cursor-pointer"
                  // The domain link is the keyboard and screen-reader path; the
                  // row click is a larger target for mouse and touch.
                  onClick={(e) => {
                    if ((e.target as HTMLElement).closest("a")) return
                    void navigate({ to: "/sites/$domain", params: { domain: site.domain } })
                  }}
                >
                  <td className="px-3 py-1.5 pointer-coarse:py-3">
                    <Link
                      to="/sites/$domain"
                      params={{ domain: site.domain }}
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
                  <td className="px-3 py-1.5 font-mono text-xs break-all">
                    {site.kind === "proxy" ? site.proxy_target : site.root}
                    {site.kind === "proxy" && appByURL.has(site.proxy_target ?? "") && (
                      <span className="text-muted-foreground block font-sans">
                        App: {appByURL.get(site.proxy_target ?? "")}
                      </span>
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
      )}

      <CreateSiteDialog
        open={creating}
        initial={proxy === undefined ? undefined : { kind: "proxy", target: proxy }}
        onOpenChange={(open) => {
          setCreating(open)
          // Drop the parameter so a reload does not reopen a dialog that was closed.
          if (!open && proxy !== undefined) void navigate({ to: "/sites", search: {}, replace: true })
        }}
        onCreated={(domain) => void navigate({ to: "/sites/$domain", params: { domain } })}
      />

      {openDomain !== undefined && (
        <SiteDetailDialog
          key={openDomain}
          domain={openDomain}
          wafPrefill={waf_rule === undefined ? undefined : { rule: waf_rule, path: waf_path }}
          onClose={() => void navigate({ to: "/sites" })}
        />
      )}
    </div>
  )
}
