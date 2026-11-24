import { useState } from "react"

import { ConfirmDialog } from "@/components/ConfirmDialog"
import { ErrorState } from "@/components/ErrorState"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { SkeletonRows } from "@/components/ui/skeleton"
import { TableFilter, TablePagination, TableToolbar, usePagedRows } from "@/components/ui/table-controls"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { describeError } from "@/lib/errors"
import { matchesQuery } from "@/lib/table"
import {
  ACTION_LABELS,
  formatPercent,
  imageLabel,
  portLabel,
  shortId,
  stateTone,
  uniquePorts,
  type DockerAction,
  type DockerContainer,
  type DockerImage,
  type DockerVolume,
} from "@/lib/docker"
import {
  useDockerContainers,
  useDockerControl,
  useDockerImages,
  useDockerInfo,
  useDockerLogs,
  useDockerProcesses,
  useDockerRemoveContainer,
  useDockerRemoveImage,
  useDockerRemoveVolume,
  useDockerStats,
  useDockerVolumes,
} from "@/lib/queries"
import { formatBytes } from "@/lib/sysinfo"

function errorText(error: unknown, fallback: string): string {
  return describeError(error, fallback)
}

const TH = "px-3 py-1.5 font-medium"
const TD = "px-3 py-1.5"

function EmptyNote({ title, children }: { title: string; children: string }) {
  return (
    <div className="border-border flex flex-col gap-2 rounded-md border border-dashed p-6">
      <p className="text-sm font-medium">{title}</p>
      <p className="text-muted-foreground text-sm">{children}</p>
    </div>
  )
}

/** Docker: what is running and what it costs, the images and volumes on disk, and container controls. */
export function DockerPage() {
  const info = useDockerInfo()
  const [tab, setTab] = useState("containers")

  if (info.isPending) {
    return <SkeletonRows label="Loading Docker…" />
  }
  if (info.error) {
    // A stopped daemon is the common case; the server's sentence says so.
    return (
      <ErrorState
        error={info.error}
        fallback="Docker could not be reached."
        onRetry={() => void info.refetch()}
      />
    )
  }
  const d = info.data

  return (
    <div className="flex flex-col gap-4">
      <dl
        className="border-border grid grid-cols-2 gap-x-6 gap-y-2 rounded-md border p-3 text-sm sm:grid-cols-3 lg:grid-cols-6"
        aria-label="Docker engine"
      >
        <Fact label="Engine" value={d.serverVersion} />
        <Fact label="Running" value={`${d.running} of ${d.containers}`} />
        <Fact label="Images" value={String(d.images)} />
        <Fact label="CPUs" value={String(d.cpus)} />
        <Fact label="Memory" value={formatBytes(d.memTotal)} />
        <Fact label="Storage" value={d.storageDriver} title={d.rootDir} />
      </dl>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="containers">Containers</TabsTrigger>
          <TabsTrigger value="monitor">Monitor</TabsTrigger>
          <TabsTrigger value="images">Images</TabsTrigger>
          <TabsTrigger value="volumes">Volumes</TabsTrigger>
        </TabsList>
        <TabsContent value="containers">
          <ContainersTab />
        </TabsContent>
        <TabsContent value="monitor">
          <MonitorTab active={tab === "monitor"} />
        </TabsContent>
        <TabsContent value="images">
          <ImagesTab />
        </TabsContent>
        <TabsContent value="volumes">
          <VolumesTab />
        </TabsContent>
      </Tabs>
    </div>
  )
}

function Fact({ label, value, title }: { label: string; value: string; title?: string }) {
  return (
    <div title={title}>
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className="font-medium">{value}</dd>
    </div>
  )
}

/* --- Containers --------------------------------------------------------- */

function ContainersTab() {
  const list = useDockerContainers()
  const control = useDockerControl()
  const remove = useDockerRemoveContainer()
  const [inspect, setInspect] = useState<DockerContainer | null>(null)
  const [confirming, setConfirming] = useState<{ c: DockerContainer; kind: "kill" | "remove" } | null>(null)
  const [pendingId, setPendingId] = useState<string | null>(null)
  const [filter, setFilter] = useState("")
  const [stateFilter, setStateFilter] = useState("all")
  const allContainers = list.data ?? []
  const shownContainers = allContainers.filter(
    (c) =>
      (stateFilter === "all" || c.state === stateFilter) &&
      matchesQuery([c.name, c.image, c.id, c.status, ...uniquePorts(c.ports).map(portLabel)], filter),
  )
  const pager = usePagedRows(shownContainers, `${filter}|${stateFilter}`)
  const stateOptions = [...new Set(allContainers.map((c) => c.state))].map((s) => ({ value: s, label: s }))

  if (list.isPending) return <SkeletonRows label="Loading containers…" />
  if (list.error) {
    return <ErrorState error={list.error} fallback="The containers could not be loaded." onRetry={() => void list.refetch()} />
  }

  const run = (c: DockerContainer, action: DockerAction) => {
    setPendingId(c.id)
    control.mutate({ id: c.id, action }, { onSettled: () => setPendingId(null) })
  }

  if (list.data.length === 0) {
    return <EmptyNote title="No containers">There are no containers on this host, running or stopped.</EmptyNote>
  }

  return (
    <div className="flex flex-col gap-3">
      {control.error && !confirming && (
        <p role="alert" className="text-destructive text-sm">
          {errorText(control.error, "The action failed.")}
        </p>
      )}
      <TableToolbar search={filter} onSearchChange={setFilter} searchLabel="Filter containers" placeholder="Filter containers">
        <TableFilter label="State" value={stateFilter} onChange={setStateFilter} options={stateOptions} />
      </TableToolbar>
      {pager.total === 0 ? (
        <p className="text-muted-foreground text-sm" role="status">
          No rows match the filters.
        </p>
      ) : (
      <div className="border-border overflow-x-auto rounded-md border">
        <table className="w-full text-sm">
          <caption className="sr-only">Containers</caption>
          <thead className="bg-muted text-muted-foreground text-left text-xs">
            <tr>
              <th scope="col" className={TH}>Name</th>
              <th scope="col" className={TH}>Image</th>
              <th scope="col" className={TH}>State</th>
              <th scope="col" className={TH}>Ports</th>
              <th scope="col" className={TH}>
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody className="divide-border divide-y">
            {pager.rows.map((c) => {
              const running = c.state === "running" || c.state === "restarting"
              const busy = pendingId === c.id
              return (
                <tr key={c.id} data-testid="docker-container-row" data-container={c.name}>
                  <td className={TD}>
                    <p className="font-medium">
                      {c.name}
                      {c.managed && (
                        <Badge variant="info" className="ml-2">
                          Panel
                        </Badge>
                      )}
                    </p>
                    <p className="text-muted-foreground font-mono text-xs">{shortId(c.id)}</p>
                  </td>
                  <td className={`${TD} max-w-56 truncate font-mono text-xs`} title={c.image}>
                    {c.image}
                  </td>
                  <td className={TD}>
                    <Badge variant={stateTone(c.state)}>{c.state}</Badge>
                    <p className="text-muted-foreground mt-1 text-xs">{c.status}</p>
                  </td>
                  <td className={`${TD} font-mono text-xs`}>
                    {uniquePorts(c.ports).map((p) => (
                      <p key={portLabel(p)}>{portLabel(p)}</p>
                    ))}
                  </td>
                  <td className={TD}>
                    <div className="flex flex-wrap justify-end gap-1.5">
                      {running ? (
                        <>
                          <Button size="sm" variant="outline" disabled={busy} aria-label={`Restart ${c.name}`} onClick={() => run(c, "restart")}>
                            {ACTION_LABELS.restart}
                          </Button>
                          {!c.managed && (
                            <>
                              <Button size="sm" variant="outline" disabled={busy} aria-label={`Stop ${c.name}`} onClick={() => run(c, "stop")}>
                                {ACTION_LABELS.stop}
                              </Button>
                              <Button size="sm" variant="outline" disabled={busy} aria-label={`Kill ${c.name}`} onClick={() => setConfirming({ c, kind: "kill" })}>
                                {ACTION_LABELS.kill}
                              </Button>
                            </>
                          )}
                        </>
                      ) : (
                        <Button size="sm" variant="outline" disabled={busy} aria-label={`Start ${c.name}`} onClick={() => run(c, "start")}>
                          {ACTION_LABELS.start}
                        </Button>
                      )}
                      <Button size="sm" variant="outline" aria-label={`Details of ${c.name}`} onClick={() => setInspect(c)}>
                        Details
                      </Button>
                      {!c.managed && (
                        <Button size="sm" variant="outline" aria-label={`Remove ${c.name}`} onClick={() => setConfirming({ c, kind: "remove" })}>
                          Remove
                        </Button>
                      )}
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

      <DetailsDialog container={inspect} onClose={() => setInspect(null)} />

      <ConfirmDialog
        open={confirming !== null}
        onOpenChange={(open) => {
          if (!open) {
            setConfirming(null)
            remove.reset()
            control.reset()
          }
        }}
        title={confirming?.kind === "kill" ? `Kill ${confirming.c.name}?` : `Remove ${confirming?.c.name ?? "container"}?`}
        confirmLabel={confirming?.kind === "kill" ? "Kill container" : "Remove container"}
        pendingLabel={confirming?.kind === "kill" ? "Killing…" : "Removing…"}
        pending={remove.isPending || control.isPending}
        destructive
        error={
          confirming?.kind === "remove" && remove.error
            ? errorText(remove.error, "The container could not be removed.")
            : confirming?.kind === "kill" && control.error
              ? errorText(control.error, "The container could not be killed.")
              : null
        }
        onConfirm={() => {
          if (!confirming) return
          const { c, kind } = confirming
          if (kind === "kill") {
            control.mutate({ id: c.id, action: "kill" }, { onSuccess: () => setConfirming(null) })
          } else {
            // Force, so a running container is removed in one step; the dialog already said so.
            remove.mutate({ id: c.id, force: true }, { onSuccess: () => setConfirming(null) })
          }
        }}
      >
        {confirming?.kind === "kill" ? (
          <>
            <p>The container&apos;s main process is killed without a chance to shut down cleanly.</p>
            <p>Use Stop instead unless it is not responding.</p>
          </>
        ) : (
          <>
            <p>A running container is killed first, then removed with its anonymous volumes.</p>
            <p>Named volumes and the image are kept. This cannot be undone.</p>
          </>
        )}
      </ConfirmDialog>
    </div>
  )
}

function DetailsDialog({ container, onClose }: { container: DockerContainer | null; onClose: () => void }) {
  const id = container?.id ?? null
  const running = container?.state === "running"
  return (
    <Dialog open={container !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{container?.name}</DialogTitle>
          <DialogDescription className="font-mono text-xs">{container?.image}</DialogDescription>
        </DialogHeader>
        {container && (
          <Tabs defaultValue="logs">
            <TabsList>
              <TabsTrigger value="logs">Logs</TabsTrigger>
              <TabsTrigger value="processes">Processes</TabsTrigger>
              <TabsTrigger value="info">Info</TabsTrigger>
            </TabsList>
            <TabsContent value="logs">
              <LogsPane id={id} />
            </TabsContent>
            <TabsContent value="processes">
              {running ? (
                <ProcessPane id={id} />
              ) : (
                <p className="text-muted-foreground text-sm">The container is not running, so it has no processes.</p>
              )}
            </TabsContent>
            <TabsContent value="info">
              <dl className="grid grid-cols-[8rem_1fr] gap-y-1.5 text-sm">
                <dt className="text-muted-foreground">ID</dt>
                <dd className="font-mono text-xs break-all">{container.id}</dd>
                <dt className="text-muted-foreground">Command</dt>
                <dd className="font-mono text-xs break-all">{container.command}</dd>
                <dt className="text-muted-foreground">Created</dt>
                <dd>{new Date(container.created).toLocaleString()}</dd>
                <dt className="text-muted-foreground">Volumes</dt>
                <dd>{container.volumes.length ? container.volumes.join(", ") : "None"}</dd>
              </dl>
            </TabsContent>
          </Tabs>
        )}
      </DialogContent>
    </Dialog>
  )
}

function LogsPane({ id }: { id: string | null }) {
  const logs = useDockerLogs(id, 200)
  if (logs.isPending) return <SkeletonRows label="Loading logs…" />
  if (logs.error) return <ErrorState error={logs.error} fallback="The logs could not be loaded." onRetry={() => void logs.refetch()} />
  return (
    <div className="flex flex-col gap-1.5">
      <pre
        tabIndex={0}
        aria-label="Container logs"
        className="bg-muted max-h-96 overflow-auto rounded-md p-3 font-mono text-xs whitespace-pre-wrap"
      >
        {logs.data.logs || "The container has written nothing."}
      </pre>
      <p className="text-muted-foreground text-xs">
        The last 200 lines, refreshed every few seconds.
        {logs.data.truncated && " The output was cut at 1 MiB."}
      </p>
    </div>
  )
}

function ProcessPane({ id }: { id: string | null }) {
  const top = useDockerProcesses(id)
  const [filter, setFilter] = useState("")
  const shownProcesses = (top.data?.processes ?? []).filter((row) => matchesQuery(row, filter))
  const pager = usePagedRows(shownProcesses, filter)
  if (top.isPending) return <SkeletonRows label="Loading processes…" />
  if (top.error) return <ErrorState error={top.error} fallback="The processes could not be loaded." onRetry={() => void top.refetch()} />
  return (
    <div className="flex flex-col">
      <TableToolbar search={filter} onSearchChange={setFilter} searchLabel="Filter processes" placeholder="Filter processes" />
      {pager.total === 0 ? (
        <p className="text-muted-foreground text-sm" role="status">
          No rows match the filters.
        </p>
      ) : (
    <div className="border-border overflow-x-auto rounded-md border">
      <table className="w-full text-xs">
        <caption className="sr-only">Processes in the container</caption>
        <thead className="bg-muted text-muted-foreground text-left">
          <tr>
            {top.data.titles.map((t) => (
              <th key={t} scope="col" className="px-2 py-1 font-medium">
                {t}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-border divide-y font-mono">
          {pager.rows.map((row, i) => (
            <tr key={i}>
              {row.map((cell, j) => (
                <td key={j} className="px-2 py-1 whitespace-nowrap">
                  {cell}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
      )}
      <TablePagination pager={pager} />
    </div>
  )
}

/* --- Monitor ------------------------------------------------------------ */

function Meter({ percent, label }: { percent: number; label: string }) {
  const value = Math.max(0, Math.min(100, percent))
  return (
    <div
      role="meter"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(value)}
      className="bg-muted h-1.5 w-24 overflow-hidden rounded-full"
    >
      <div className="bg-primary h-full" style={{ width: `${value}%` }} />
    </div>
  )
}

function MonitorTab({ active }: { active: boolean }) {
  // Sampling costs the host about a second per container, so only the visible tab polls.
  const stats = useDockerStats(active)
  const [filter, setFilter] = useState("")
  const rows = (stats.data ?? [])
    .filter((s) => matchesQuery([s.name, s.id], filter))
    .sort((a, b) => b.cpuPercent - a.cpuPercent)
  const pager = usePagedRows(rows, filter)
  if (stats.isPending) return <SkeletonRows label="Sampling containers…" />
  if (stats.error) return <ErrorState error={stats.error} fallback="The containers could not be sampled." onRetry={() => void stats.refetch()} />
  if (stats.data.length === 0) {
    return <EmptyNote title="Nothing is running">Start a container to see its CPU, memory and I/O here.</EmptyNote>
  }
  return (
    <div className="flex flex-col gap-2">
      <TableToolbar search={filter} onSearchChange={setFilter} searchLabel="Filter containers" placeholder="Filter containers" />
      {pager.total === 0 ? (
        <p className="text-muted-foreground text-sm" role="status">
          No rows match the filters.
        </p>
      ) : (
      <div className="border-border overflow-x-auto rounded-md border">
        <table className="w-full text-sm">
          <caption className="sr-only">Resource use of running containers</caption>
          <thead className="bg-muted text-muted-foreground text-left text-xs">
            <tr>
              <th scope="col" className={TH}>Container</th>
              <th scope="col" className={TH}>CPU</th>
              <th scope="col" className={TH}>Memory</th>
              <th scope="col" className={TH}>Network in / out</th>
              <th scope="col" className={TH}>Disk read / write</th>
              <th scope="col" className={TH}>Processes</th>
            </tr>
          </thead>
          <tbody className="divide-border divide-y">
            {pager.rows.map((s) => (
              <tr key={s.id} data-testid="docker-stats-row" data-container={s.name}>
                <td className={`${TD} font-medium`}>{s.name}</td>
                <td className={TD}>
                  <div className="flex items-center gap-2">
                    <Meter percent={s.cpuPercent} label={`CPU of ${s.name}`} />
                    <span className="tabular-nums">{formatPercent(s.cpuPercent)}</span>
                  </div>
                </td>
                <td className={TD}>
                  <div className="flex items-center gap-2">
                    <Meter percent={s.memPercent} label={`Memory of ${s.name}`} />
                    <span className="text-xs tabular-nums whitespace-nowrap">
                      {formatBytes(s.memUsage)}
                      {s.memLimit > 0 && ` / ${formatBytes(s.memLimit)}`}
                    </span>
                  </div>
                </td>
                <td className={`${TD} text-xs tabular-nums whitespace-nowrap`}>
                  {formatBytes(s.netRx)} / {formatBytes(s.netTx)}
                </td>
                <td className={`${TD} text-xs tabular-nums whitespace-nowrap`}>
                  {formatBytes(s.blockRead)} / {formatBytes(s.blockWrite)}
                </td>
                <td className={`${TD} tabular-nums`}>{s.pids}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      )}
      <TablePagination pager={pager} />
      <p className="text-muted-foreground text-xs">
        CPU is a share of one core per 100%. Network and disk are totals since the container started.
      </p>
    </div>
  )
}

/* --- Images ------------------------------------------------------------- */

function ImagesTab() {
  const list = useDockerImages()
  const remove = useDockerRemoveImage()
  const [deleting, setDeleting] = useState<DockerImage | null>(null)
  const [filter, setFilter] = useState("")
  const [usedFilter, setUsedFilter] = useState("all")
  const shownImages = (list.data ?? []).filter(
    (im) =>
      (usedFilter === "all" || (usedFilter === "used") === (im.inUseBy.length > 0)) &&
      matchesQuery([...im.tags, im.id, ...im.inUseBy], filter),
  )
  const pager = usePagedRows(shownImages, `${filter}|${usedFilter}`)

  if (list.isPending) return <SkeletonRows label="Loading images…" />
  if (list.error) return <ErrorState error={list.error} fallback="The images could not be loaded." onRetry={() => void list.refetch()} />
  if (list.data.length === 0) return <EmptyNote title="No images">No images are stored on this host.</EmptyNote>

  return (
    <div className="flex flex-col gap-3">
      <TableToolbar search={filter} onSearchChange={setFilter} searchLabel="Filter images" placeholder="Filter images">
        <TableFilter
          label="Use"
          value={usedFilter}
          onChange={setUsedFilter}
          options={[
            { value: "used", label: "In use" },
            { value: "unused", label: "Not used" },
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
          <caption className="sr-only">Images</caption>
          <thead className="bg-muted text-muted-foreground text-left text-xs">
            <tr>
              <th scope="col" className={TH}>Image</th>
              <th scope="col" className={TH}>Size</th>
              <th scope="col" className={TH}>Created</th>
              <th scope="col" className={TH}>Used by</th>
              <th scope="col" className={TH}>
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody className="divide-border divide-y">
            {pager.rows.map((im) => (
              <tr key={im.id} data-testid="docker-image-row" data-image={imageLabel(im)}>
                <td className={TD}>
                  {im.tags.length === 0 ? (
                    <p>
                      <Badge variant="outline">Untagged</Badge>
                    </p>
                  ) : (
                    im.tags.map((t) => (
                      <p key={t} className="font-mono text-xs">
                        {t}
                      </p>
                    ))
                  )}
                  <p className="text-muted-foreground font-mono text-xs">{shortId(im.id)}</p>
                </td>
                <td className={`${TD} whitespace-nowrap tabular-nums`}>{formatBytes(im.size)}</td>
                <td className={`${TD} whitespace-nowrap`}>{new Date(im.created).toLocaleDateString()}</td>
                <td className={TD}>
                  {im.inUseBy.length ? im.inUseBy.join(", ") : <span className="text-muted-foreground">Not used</span>}
                  {im.managed && (
                    <Badge variant="info" className="ml-2">
                      Panel
                    </Badge>
                  )}
                </td>
                <td className={TD}>
                  <div className="flex justify-end">
                    {!im.managed && (
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={im.inUseBy.length > 0}
                        title={im.inUseBy.length ? "Remove the containers that use it first" : undefined}
                        aria-label={`Remove ${imageLabel(im)}`}
                        onClick={() => setDeleting(im)}
                      >
                        Remove
                      </Button>
                    )}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      )}
      <TablePagination pager={pager} />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) {
            setDeleting(null)
            remove.reset()
          }
        }}
        title={`Remove ${deleting ? imageLabel(deleting) : "image"}?`}
        confirmLabel="Remove image"
        pendingLabel="Removing…"
        pending={remove.isPending}
        destructive
        error={remove.error ? errorText(remove.error, "The image could not be removed.") : null}
        onConfirm={() => deleting && remove.mutate(deleting.id, { onSuccess: () => setDeleting(null) })}
      >
        <p>The image is deleted from this host. Pulling or building it again takes time and bandwidth.</p>
      </ConfirmDialog>
    </div>
  )
}

/* --- Volumes ------------------------------------------------------------ */

function VolumesTab() {
  const list = useDockerVolumes()
  const remove = useDockerRemoveVolume()
  const [deleting, setDeleting] = useState<DockerVolume | null>(null)
  const [filter, setFilter] = useState("")
  const [usedFilter, setUsedFilter] = useState("all")
  const shownVolumes = (list.data ?? []).filter(
    (v) =>
      (usedFilter === "all" || (usedFilter === "used") === (v.inUseBy.length > 0)) &&
      matchesQuery([v.name, v.driver, v.mountpoint, ...v.inUseBy], filter),
  )
  const pager = usePagedRows(shownVolumes, `${filter}|${usedFilter}`)

  if (list.isPending) return <SkeletonRows label="Loading volumes…" />
  if (list.error) return <ErrorState error={list.error} fallback="The volumes could not be loaded." onRetry={() => void list.refetch()} />
  if (list.data.length === 0) return <EmptyNote title="No volumes">No named volumes exist on this host.</EmptyNote>

  return (
    <div className="flex flex-col gap-3">
      <TableToolbar search={filter} onSearchChange={setFilter} searchLabel="Filter volumes" placeholder="Filter volumes">
        <TableFilter
          label="Use"
          value={usedFilter}
          onChange={setUsedFilter}
          options={[
            { value: "used", label: "In use" },
            { value: "unused", label: "Not used" },
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
          <caption className="sr-only">Volumes</caption>
          <thead className="bg-muted text-muted-foreground text-left text-xs">
            <tr>
              <th scope="col" className={TH}>Name</th>
              <th scope="col" className={TH}>Driver</th>
              <th scope="col" className={TH}>Mountpoint</th>
              <th scope="col" className={TH}>Used by</th>
              <th scope="col" className={TH}>
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody className="divide-border divide-y">
            {pager.rows.map((v) => (
              <tr key={v.name} data-testid="docker-volume-row" data-volume={v.name}>
                <td className={`${TD} font-medium break-all`}>{v.name}</td>
                <td className={TD}>{v.driver}</td>
                <td className={`${TD} font-mono text-xs break-all`}>{v.mountpoint}</td>
                <td className={TD}>
                  {v.inUseBy.length ? v.inUseBy.join(", ") : <span className="text-muted-foreground">Not used</span>}
                </td>
                <td className={TD}>
                  <div className="flex justify-end">
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={v.inUseBy.length > 0}
                      title={v.inUseBy.length ? "Remove the containers that use it first" : undefined}
                      aria-label={`Remove ${v.name}`}
                      onClick={() => setDeleting(v)}
                    >
                      Remove
                    </Button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      )}
      <TablePagination pager={pager} />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) {
            setDeleting(null)
            remove.reset()
          }
        }}
        title={`Remove ${deleting?.name ?? "volume"}?`}
        confirmLabel="Remove volume"
        pendingLabel="Removing…"
        pending={remove.isPending}
        destructive
        error={remove.error ? errorText(remove.error, "The volume could not be removed.") : null}
        onConfirm={() => deleting && remove.mutate(deleting.name, { onSuccess: () => setDeleting(null) })}
      >
        <p>Everything stored in the volume is deleted. This cannot be undone.</p>
      </ConfirmDialog>
    </div>
  )
}
