import { Link } from "@tanstack/react-router"
import { Plus } from "lucide-react"
import { useState } from "react"

import { AppDialog, type AppSection, type AppSiteNav } from "@/components/AppDialog"
import { AppLogs } from "@/components/AppLogs"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { CreateSiteDialog } from "@/components/CreateSiteDialog"
import { TLSCell } from "@/components/TLSCell"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Select } from "@/components/ui/select"
import { TableFilter, TablePagination, TableToolbar, usePagedRows } from "@/components/ui/table-controls"
import { ApiError, type AppAction, type AppType, type CertView, type PanelApp, type Site } from "@/lib/api"
import { useControlApp, useDeleteApp } from "@/lib/queries"
import { matchesQuery } from "@/lib/table"
import { tabForApp } from "@/lib/sitesTabs"
import { cn } from "@/lib/utils"
import { SitePane, siteSectionList, type SiteSection } from "@/pages/SiteDetailPage"

export function typeLabel(app: Pick<PanelApp, "type" | "node_version">): string {
  if (app.type === "node") return `Node.js ${app.node_version}`
  return app.type === "go" ? "Program" : "Command"
}

/** "3m 20s", "2h 5m", "4d 1h": the two largest units, which is all a glance needs. */
export function formatUptime(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  const m = Math.floor(seconds / 60)
  if (m < 60) return `${m}m ${seconds % 60}s`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ${m % 60}m`
  return `${Math.floor(h / 24)}d ${h % 24}h`
}

/** The sites that pass requests to an app's port: as the whole site or as a subpath. */
export function sitesOf(app: Pick<PanelApp, "proxy_url">, sites: Site[]): Site[] {
  if (app.proxy_url === "") return []
  return sites.filter(
    (s) =>
      (s.kind === "proxy" && s.proxy_target === app.proxy_url) ||
      (s.proxy_routes ?? []).some((r) => r.target === app.proxy_url),
  )
}

/** The domains of the sites that pass requests to an app's port. */
export function domainsOf(app: Pick<PanelApp, "proxy_url">, sites: Site[]): string[] {
  return sitesOf(app, sites).map((s) => s.domain)
}

/** The subpath a site hands to an app, or "" when the whole site goes to it. */
function pathOnSite(app: Pick<PanelApp, "proxy_url">, site: Site): string {
  if (site.kind === "proxy" && site.proxy_target === app.proxy_url) return ""
  const route = (site.proxy_routes ?? []).find((r) => r.target === app.proxy_url)
  return route && route.path !== "/" ? route.path : ""
}

export function isRunning(app: Pick<PanelApp, "state">): boolean {
  return app.state === "RUNNING" || app.state === "STARTING"
}

/**
 * The apps of one type and the sites in front of them, in one table: state and
 * controls, and under Domains each site with its HTTPS state. Manage opens one dialog
 * whose sidebar holds the app's settings and log, the domain menu (manager, reverse
 * proxy, config, SSL…) of the domains in front of it, and delete.
 */
export function AppsTable({
  type,
  apps,
  sites,
  certs,
  certsPending,
  intro,
  empty,
}: {
  type: AppType
  /** Every app of the panel; this shows the ones of `type`. */
  apps: PanelApp[]
  sites: Site[]
  /** Certificates by domain; a domain without one shows "No HTTPS". */
  certs: Map<string, CertView>
  certsPending: boolean
  /** What this kind of app is, shown above the table. */
  intro: string
  /** Shown when there are no apps of this type. */
  empty: string
}) {
  const control = useControlApp()
  const remove = useDeleteApp()
  // `name` null is a new app. `site` is the domain the dialog's domain menu is on,
  // when the app has several. The app itself is looked up each render, so the
  // dialog follows the list as it refreshes.
  const [managing, setManaging] = useState<{ name: string | null; section: AppSection; site?: string } | null>(null)
  const pickSite = (site: string) => setManaging((m) => (m === null ? m : { ...m, site }))
  const [deleting, setDeleting] = useState<PanelApp | null>(null)
  const [addingDomainTo, setAddingDomainTo] = useState<PanelApp | null>(null)

  const rows = apps.filter((app) => app.type === type)
  const [filter, setFilter] = useState("")
  const [runFilter, setRunFilter] = useState("all")
  const shown = rows.filter(
    (app) =>
      (runFilter === "all" || (runFilter === "running") === isRunning(app)) &&
      matchesQuery(
        [app.name, typeLabel(app), app.state, app.port, ...sitesOf(app, sites).map((s) => s.domain)],
        filter,
      ),
  )
  const pager = usePagedRows(shown, `${type}|${filter}|${runFilter}`)
  const current = managing?.name == null ? null : (apps.find((a) => a.name === managing.name) ?? null)

  const act = (name: string, action: AppAction) => {
    // Starting shows the app's log while supervisord works, and the dialog stays
    // open afterwards so a failed start can be read, never closed on its own.
    if (action !== "stop") setManaging((m) => ({ name, section: "logs", site: m?.name === name ? m.site : undefined }))
    control.mutate({ name, action })
  }
  const closeManage = () => {
    setManaging(null)
    control.reset()
  }
  const dialogError =
    current !== null && control.variables?.name === current.name && control.error
      ? control.error instanceof ApiError
        ? control.error.detail
        : "The action failed."
      : null
  const pendingHere = current !== null && control.isPending && control.variables?.name === current.name

  // The domains in front of the open app. One dialog configures all of them: the
  // domain menu is on `activeSite`, and a picker switches it, so no domain opens
  // a dialog of its own.
  const mine = current === null ? [] : sitesOf(current, sites)
  const activeSite = mine.find((s) => s.domain === managing?.site) ?? mine[0]
  const siteNav: AppSiteNav | undefined =
    current === null
      ? undefined
      : activeSite === undefined
        ? [{ id: "site:domains", label: "Domains" }]
        : siteSectionList(false).map((i) => ({
            id: `site:${i.id}` as AppSection,
            label: i.id === "domains" ? "Domains" : i.id === "logs" ? "Site logs" : i.label,
          }))

  const renderSection = (section: AppSection, app: PanelApp) => {
    if (section.startsWith("site:")) {
      // A section the active domain does not have (the PHP ones, after switching
      // domains) falls back to the manager.
      const id: SiteSection = siteNav?.some((i) => i.id === section)
        ? (section.slice("site:".length) as SiteSection)
        : "domains"
      return (
        <div className="flex flex-col gap-4">
          {mine.length > 1 && activeSite !== undefined && (
            <div className="flex flex-wrap items-center gap-2">
              <label htmlFor="app-domain" className="text-muted-foreground text-sm">
                Domain
              </label>
              <Select
                id="app-domain"
                className="w-auto min-w-48"
                value={activeSite.domain}
                onChange={(e) => pickSite(e.target.value)}
              >
                {mine.map((s) => (
                  <option key={s.domain} value={s.domain}>
                    {s.domain}
                    {pathOnSite(app, s)}
                  </option>
                ))}
              </Select>
            </div>
          )}
          {id === "domains" && (
            <div className="flex flex-col gap-4">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <p className="text-muted-foreground max-w-md text-sm">
                  Sites that pass requests to this app{app.port == null ? "" : ` on port ${app.port}`}.
                </p>
                <Button size="sm" disabled={app.proxy_url === ""} onClick={() => setAddingDomainTo(app)}>
                  <Plus aria-hidden /> Add domain
                </Button>
              </div>
              {app.proxy_url === "" && (
                <p className="text-muted-foreground text-sm">
                  Set a port under Settings first: a domain passes requests to it.
                </p>
              )}
              {mine.length === 0 ? (
                app.proxy_url !== "" && (
                  <p className="text-muted-foreground border-border rounded-md border border-dashed p-6 text-sm">
                    No domain points at this app yet.
                  </p>
                )
              ) : (
                <ul className="divide-border border-border divide-y rounded-md border">
                  {mine.map((s) => (
                    <li key={s.domain}>
                      <button
                        type="button"
                        aria-current={s.domain === activeSite?.domain ? "true" : undefined}
                        onClick={() => pickSite(s.domain)}
                        className={cn(
                          "focus-visible:ring-ring flex w-full flex-wrap items-center justify-between gap-2 px-3 py-2 text-left text-sm outline-none focus-visible:ring-2",
                          s.domain === activeSite?.domain ? "bg-accent/60" : "hover:bg-accent/30",
                        )}
                      >
                        <span className="flex flex-col gap-0.5">
                          <span className="font-medium">
                            {s.domain}
                            <span className="text-muted-foreground font-mono text-xs">{pathOnSite(app, s)}</span>
                          </span>
                          {s.aliases.length > 0 && (
                            <span className="text-muted-foreground text-xs">{s.aliases.join(", ")}</span>
                          )}
                        </span>
                        {certsPending ? (
                          <span className="text-muted-foreground">…</span>
                        ) : (
                          <TLSCell cert={certs.get(s.domain)} />
                        )}
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
          {activeSite !== undefined && (
            <SitePane
              domain={activeSite.domain}
              section={id}
              onGone={() =>
                setManaging((m) => (m === null ? m : { ...m, section: "site:domains", site: undefined }))
              }
            />
          )}
        </div>
      )
    }
    if (section === "logs") {
      return (
        <div className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-muted-foreground text-sm">Output and errors, newest at the bottom.</p>
            <div className="flex gap-1.5">
              {isRunning(app) ? (
                <Button size="sm" variant="outline" disabled={control.isPending} onClick={() => act(app.name, "stop")}>
                  Stop
                </Button>
              ) : (
                <Button size="sm" variant="outline" disabled={control.isPending} onClick={() => act(app.name, "start")}>
                  Start
                </Button>
              )}
              <Button size="sm" variant="outline" disabled={control.isPending} onClick={() => act(app.name, "restart")}>
                Restart
              </Button>
            </div>
          </div>
          {pendingHere && (
            <p role="status" className="text-muted-foreground text-sm">
              {control.variables?.action === "restart" ? "Restarting…" : "Starting…"}
            </p>
          )}
          {dialogError && (
            <p role="alert" className="text-destructive text-sm">
              {dialogError}
            </p>
          )}
          <AppLogs name={app.name} />
        </div>
      )
    }
    // delete
    return (
      <div className="flex flex-col gap-3">
        <p className="text-sm">
          Deleting {app.name} stops it and removes its program file and log directory. Any reverse-proxy site that
          passes requests to its port is removed too. Its working directory is left in place.
        </p>
        <div>
          <Button variant="destructive" onClick={() => setDeleting(app)}>
            Delete app
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <p className="text-muted-foreground max-w-2xl text-sm">{intro}</p>
        <Button onClick={() => setManaging({ name: null, section: "settings" })}>
          <Plus aria-hidden /> Add app
        </Button>
      </div>

      {control.error && managing === null && (
        <p role="alert" className="text-destructive text-sm">
          {control.error instanceof ApiError ? control.error.detail : "The action failed."}
        </p>
      )}

      {rows.length === 0 ? (
        <p className="text-muted-foreground border-border rounded-md border border-dashed p-6 text-sm">{empty}</p>
      ) : (
        <div className="flex flex-col">
          <TableToolbar search={filter} onSearchChange={setFilter} searchLabel="Filter apps" placeholder="Filter apps…">
            <TableFilter
              label="State"
              value={runFilter}
              onChange={setRunFilter}
              options={[
                { value: "running", label: "Running" },
                { value: "stopped", label: "Not running" },
              ]}
            />
          </TableToolbar>
          {pager.total === 0 ? (
            <p className="text-muted-foreground text-sm" role="status">
              No rows match the filters.
            </p>
          ) : (
        <div className="border-border overflow-x-auto rounded-md border">
          <table className="w-full text-sm">
            <caption className="sr-only">Apps of this type and the domains in front of them</caption>
            <thead className="bg-muted text-muted-foreground text-left text-xs">
              <tr>
                <th scope="col" className="px-3 py-1.5 font-medium">Name</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Kind</th>
                <th scope="col" className="px-3 py-1.5 font-medium">State</th>
                <th scope="col" className="px-3 py-1.5 font-medium">PID</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Up for</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Port</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Domains</th>
                <th scope="col" className="px-3 py-1.5 font-medium">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {pager.rows.map((app) => {
                const mine = sitesOf(app, sites)
                return (
                  <tr key={app.name}>
                    <td className="px-3 py-1.5 font-medium">{app.name}</td>
                    <td className="text-muted-foreground px-3 py-1.5">{typeLabel(app)}</td>
                    <td className="px-3 py-1.5">
                      <Badge variant={isRunning(app) ? "secondary" : "destructive"}>{app.state}</Badge>
                      {app.spawn_error !== "" && (
                        <p className="text-destructive mt-1 max-w-xs text-xs">{app.spawn_error}</p>
                      )}
                    </td>
                    <td className="px-3 py-1.5 tabular-nums">{app.pid > 0 ? app.pid : "–"}</td>
                    <td className="px-3 py-1.5 tabular-nums">
                      {isRunning(app) && app.uptime_seconds > 0 ? formatUptime(app.uptime_seconds) : "–"}
                    </td>
                    <td className="px-3 py-1.5 tabular-nums">{app.port ?? "–"}</td>
                    <td className="px-3 py-1.5">
                      {mine.length === 0 ? (
                        <span className="text-muted-foreground">–</span>
                      ) : (
                        <ul className="flex flex-col gap-1">
                          {mine.map((s) => (
                            <li key={s.domain} className="flex flex-wrap items-center gap-2">
                              <Link
                                to="/sites/$domain"
                                params={{ domain: s.domain }}
                                search={{ tab: tabForApp(app.type) }}
                                className="underline-offset-4 hover:underline"
                              >
                                {s.domain}
                                <span className="text-muted-foreground font-mono text-xs">{pathOnSite(app, s)}</span>
                              </Link>
                              {certsPending ? (
                                <span className="text-muted-foreground">…</span>
                              ) : (
                                <TLSCell cert={certs.get(s.domain)} />
                              )}
                            </li>
                          ))}
                        </ul>
                      )}
                    </td>
                    <td className="px-3 py-1.5">
                      <div className="flex flex-wrap justify-end gap-1.5">
                        {isRunning(app) ? (
                          <Button size="sm" variant="outline" disabled={control.isPending} onClick={() => act(app.name, "stop")}>
                            Stop
                          </Button>
                        ) : (
                          <Button size="sm" variant="outline" disabled={control.isPending} onClick={() => act(app.name, "start")}>
                            Start
                          </Button>
                        )}
                        <Button size="sm" variant="outline" disabled={control.isPending} onClick={() => act(app.name, "restart")}>
                          Restart
                        </Button>
                        <Button size="sm" variant="outline" onClick={() => setManaging({ name: app.name, section: "settings" })}>
                          Manage
                        </Button>
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
          )}
          <TablePagination pager={pager} />
        </div>
      )}

      <AppDialog
        // Remounted per target so the form never shows another app's fields.
        key={managing === null ? "none" : (managing.name ?? "new")}
        open={managing !== null && (managing.name === null || current !== null)}
        onOpenChange={(open) => !open && closeManage()}
        app={current}
        defaultType={type}
        section={managing?.section ?? "settings"}
        onSectionChange={(section) => setManaging((m) => (m === null ? m : { ...m, section }))}
        renderSection={renderSection}
        siteNav={siteNav}
      />

      <CreateSiteDialog
        open={addingDomainTo !== null}
        initial={addingDomainTo === null ? undefined : { target: addingDomainTo.proxy_url }}
        onOpenChange={(open) => !open && setAddingDomainTo(null)}
        onCreated={() => setAddingDomainTo(null)}
      />

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) {
            setDeleting(null)
            remove.reset()
          }
        }}
        title={`Delete ${deleting?.name ?? "app"}?`}
        confirmLabel="Delete app"
        pendingLabel="Deleting…"
        pending={remove.isPending}
        destructive
        error={remove.error instanceof ApiError ? remove.error.detail : remove.error ? "The app could not be deleted." : null}
        onConfirm={() =>
          deleting &&
          remove.mutate(deleting.name, {
            onSuccess: () => {
              setDeleting(null)
              closeManage()
            },
          })
        }
      >
        <p>It is stopped and its program file and log directory are removed.</p>
        <p>
          Any reverse-proxy site that passes requests to its port is removed too, since it would
          point at a port nothing serves. Its working directory is left in place.
        </p>
      </ConfirmDialog>
    </div>
  )
}
