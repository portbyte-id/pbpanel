import { Link, useNavigate, useSearch } from "@tanstack/react-router"
import { Plus } from "lucide-react"
import { useState } from "react"

import { CreateSiteDialog } from "@/components/CreateSiteDialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { ApiError, type Site } from "@/lib/api"
import { useSites } from "@/lib/queries"

export function kindLabel(kind: Site["kind"]): string {
  if (kind === "php") return "PHP"
  return kind === "proxy" ? "Reverse proxy" : "Static files"
}

/** The sites list. A row leads to the site; the button adds one. */
export function SitesPage() {
  const { data, isPending, error } = useSites()
  // `?proxy=<url>` comes from an app's "Proxy site" button: the dialog opens on
  // "Another server" with the app's address filled in.
  const { proxy } = useSearch({ strict: false }) as { proxy?: string }
  const [creating, setCreating] = useState(proxy !== undefined)
  const navigate = useNavigate()

  if (isPending) {
    return <p className="text-muted-foreground text-sm">Loading sites…</p>
  }
  if (error) {
    return (
      <p role="alert" className="text-destructive text-sm">
        {error instanceof ApiError ? error.detail : "The sites could not be loaded."}
      </p>
    )
  }

  const sites = data ?? []

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-muted-foreground text-sm">
          {sites.length === 0
            ? "No sites yet."
            : `${sites.length} site${sites.length === 1 ? "" : "s"} served by nginx.`}
        </p>
        <Button onClick={() => setCreating(true)}>
          <Plus aria-hidden="true" />
          Add site
        </Button>
      </div>

      {sites.length === 0 ? (
        <div className="rounded-lg border border-dashed p-8 text-center">
          <p className="text-sm font-medium">Add your first site</p>
          <p className="text-muted-foreground mx-auto mt-1 max-w-md text-sm">
            A site answers for one or more domains. It can serve files from a folder or pass
            requests to an app running on this server.
          </p>
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-sm">
            <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
              <tr>
                <th className="px-3 py-1.5 font-medium">Domain</th>
                <th className="px-3 py-1.5 font-medium">Type</th>
                <th className="px-3 py-1.5 font-medium">Serves</th>
                <th className="px-3 py-1.5 font-medium">Other domains</th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {sites.map((site) => (
                <tr key={site.domain}>
                  <td className="px-3 py-1.5">
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
                  <td className="px-3 py-1.5 font-mono text-xs break-all">
                    {site.kind === "proxy" ? site.proxy_target : site.root}
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
    </div>
  )
}
