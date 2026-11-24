import { Link } from "@tanstack/react-router"
import { Plus } from "lucide-react"
import { useState } from "react"

import { AppDialog } from "@/components/AppDialog"
import { AppLogs } from "@/components/AppLogs"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { ApiError, type AppAction, type AppType, type PanelApp, type Site } from "@/lib/api"
import { useControlApp, useDeleteApp } from "@/lib/queries"
import { tabForApp } from "@/lib/sitesTabs"

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

/** The domains of the proxy sites that pass requests to an app's port. */
export function domainsOf(app: Pick<PanelApp, "proxy_url">, sites: Site[]): string[] {
  if (app.proxy_url === "") return []
  return sites.filter((s) => s.kind === "proxy" && s.proxy_target === app.proxy_url).map((s) => s.domain)
}

export function isRunning(app: Pick<PanelApp, "state">): boolean {
  return app.state === "RUNNING" || app.state === "STARTING"
}

/**
 * The apps of one type, with their controls, logs, and a shortcut to put a site in
 * front of one. Each app tab of the Sites page shows one of these.
 */
export function AppsTable({
  type,
  apps,
  sites,
  intro,
  empty,
}: {
  type: AppType
  /** Every app of the panel; this shows the ones of `type`. */
  apps: PanelApp[]
  sites: Site[]
  /** What this kind of app is, shown above the table. */
  intro: string
  /** Shown when there are no apps of this type. */
  empty: string
}) {
  const control = useControlApp()
  const remove = useDeleteApp()
  const [editing, setEditing] = useState<PanelApp | "new" | null>(null)
  const [logsFor, setLogsFor] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<PanelApp | null>(null)

  const rows = apps.filter((app) => app.type === type)
  const act = (name: string, action: AppAction) => control.mutate({ name, action })

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <p className="text-muted-foreground max-w-2xl text-sm">{intro}</p>
        <Button onClick={() => setEditing("new")}>
          <Plus aria-hidden /> Add app
        </Button>
      </div>

      {control.error && (
        <p role="alert" className="text-destructive text-sm">
          {control.error instanceof ApiError ? control.error.detail : "The action failed."}
        </p>
      )}

      {rows.length === 0 ? (
        <p className="text-muted-foreground border-border rounded-md border border-dashed p-6 text-sm">{empty}</p>
      ) : (
        <div className="border-border overflow-x-auto rounded-md border">
          <table className="w-full text-sm">
            <caption className="sr-only">Apps of this type</caption>
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
              {rows.map((app) => (
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
                    {domainsOf(app, sites).length === 0 ? (
                      <span className="text-muted-foreground">–</span>
                    ) : (
                      <div className="flex flex-col gap-0.5">
                        {domainsOf(app, sites).map((d) => (
                          <Link
                            key={d}
                            to="/sites/$domain"
                            params={{ domain: d }}
                            search={{ tab: tabForApp(app.type) }}
                            className="underline-offset-4 hover:underline"
                          >
                            {d}
                          </Link>
                        ))}
                      </div>
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
                      <Button size="sm" variant="outline" onClick={() => setLogsFor(app.name)}>
                        Logs
                      </Button>
                      <Button size="sm" variant="outline" onClick={() => setEditing(app)}>
                        Edit
                      </Button>
                      {app.proxy_url !== "" && (
                        <Button size="sm" variant="outline" asChild>
                          <Link to="/sites" search={{ proxy: app.proxy_url, tab: tabForApp(app.type) }}>
                            Add domain
                          </Link>
                        </Button>
                      )}
                      <Button size="sm" variant="outline" onClick={() => setDeleting(app)}>
                        Delete
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <AppDialog
        // Remounted per target so the form never shows another app's fields.
        key={editing === "new" ? "new" : (editing?.name ?? "none")}
        open={editing !== null}
        onOpenChange={(open) => !open && setEditing(null)}
        app={editing === "new" ? null : editing}
        defaultType={type}
      />

      <Dialog open={logsFor !== null} onOpenChange={(open) => !open && setLogsFor(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{logsFor} log</DialogTitle>
            <DialogDescription>Output and errors, newest at the bottom.</DialogDescription>
          </DialogHeader>
          {logsFor !== null && <AppLogs name={logsFor} />}
        </DialogContent>
      </Dialog>

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
            onSuccess: () => setDeleting(null),
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
