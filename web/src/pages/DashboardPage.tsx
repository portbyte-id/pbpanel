import { SkeletonRows } from "@/components/ui/skeleton"
import { Link } from "@tanstack/react-router"
import { ArrowRight, CircleAlert, CircleCheck, TriangleAlert } from "lucide-react"
import { useMemo, useState, type ReactNode } from "react"

import { ApplyError } from "@/components/ApplyError"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { Panel } from "@/components/Panel"
import { Sparkline } from "@/components/Sparkline"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { deriveAlerts, type AlertLink, type DashboardAlert } from "@/lib/alerts"
import { ApiError } from "@/lib/api"
import {
  useAudit,
  useControlService,
  useCronJobs,
  useFirewall,
  useServices,
  useSiteDetails,
  useSysinfoLive,
  useTLSCerts,
} from "@/lib/queries"
import {
  formatBytes,
  formatPercent,
  formatRate,
  formatUptime,
  ratioPercent,
  type ServiceAction,
  type ServiceRow,
  type Snapshot,
  type SysinfoStreamStatus,
} from "@/lib/sysinfo"
import { cn } from "@/lib/utils"

function errorText(error: unknown, fallback: string): string {
  return error instanceof ApiError ? error.detail || fallback : fallback
}

/** Warning from 75%, a problem from 90%: the point where an operator should look. */
function levelText(percent: number): string {
  if (percent >= 90) return "text-destructive"
  if (percent >= 75) return "text-warning"
  return ""
}

type LiveSysinfo = ReturnType<typeof useSysinfoLive>

/** Health at a glance: one status line, the machine's numbers, the services, what needs attention. */
export function DashboardPage() {
  // One stream for the whole page: the status line and the tiles read the same samples.
  const live = useSysinfoLive()
  const health = useDashboardAlerts()

  return (
    <div className="flex flex-col gap-3">
      <HealthBar alerts={health.alerts} unchecked={health.unchecked} checking={health.checking} live={live} />
      <MetricsSection live={live} />
      <div className="grid items-start gap-3 xl:grid-cols-[minmax(0,1fr)_26rem]">
        <ServicesSection limit={8} />
        <div className="flex flex-col gap-3">
          <AlertsPanel alerts={health.alerts} unchecked={health.unchecked} checking={health.checking} />
          {live.latest && <DisksPanel disks={live.latest.disks} />}
        </div>
      </div>
    </div>
  )
}

/* --- Health bar --------------------------------------------------------- */

function streamNote(status: SysinfoStreamStatus, seconds: number): string {
  if (status === "live") return "Live."
  if (status === "polling") return `Live stream unavailable. Refreshing every ${seconds} seconds.`
  return "Connecting…"
}

function HealthBar({
  alerts,
  unchecked,
  checking,
  live,
}: {
  alerts: DashboardAlert[]
  /** Sources that could not be read: "normal" must not be claimed over them. */
  unchecked: string[]
  checking: boolean
  live: LiveSysinfo
}) {
  const errors = alerts.filter((a) => a.severity === "error").length
  const warnings = alerts.length - errors

  let dot = "bg-success"
  let text = "All systems normal"
  if (errors > 0) {
    dot = "bg-destructive"
    text = `${errors} ${errors === 1 ? "problem" : "problems"}${warnings > 0 ? `, ${warnings} ${warnings === 1 ? "warning" : "warnings"}` : ""}`
  } else if (warnings > 0) {
    dot = "bg-warning"
    text = `${warnings} ${warnings === 1 ? "warning" : "warnings"}`
  } else if (unchecked.length > 0) {
    dot = "bg-warning"
    text = `${unchecked.length} ${unchecked.length === 1 ? "check" : "checks"} could not run`
  } else if (checking) {
    dot = "bg-muted-foreground/50"
    text = "Checking…"
  }

  return (
    <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1">
      <p role="status" className="flex items-center gap-2 text-sm font-medium">
        <span className={cn("size-2 rounded-full", dot)} aria-hidden="true" />
        {text}
      </p>
      <p className="text-muted-foreground text-xs" aria-live="polite">
        {streamNote(live.status, live.intervalSeconds)}
      </p>
    </div>
  )
}

/* --- Alerts ------------------------------------------------------------- */

function AlertLinkView({ link }: { link: AlertLink }) {
  const cls = "text-primary shrink-0 text-xs font-medium underline-offset-2 hover:underline"
  switch (link.kind) {
    case "tls":
      return (
        <Link to="/tls" className={cls}>
          Open TLS
        </Link>
      )
    case "firewall":
      return (
        <Link to="/firewall" className={cls}>
          Open the firewall
        </Link>
      )
    case "site":
      return (
        <Link to="/sites/$domain" params={{ domain: link.domain }} className={cls}>
          Open {link.domain}
        </Link>
      )
    case "cron":
      return (
        <Link to="/cron" className={cls}>
          Open cron
        </Link>
      )
    case "audit":
      return (
        <Link to="/audit" className={cls}>
          Open the audit log
        </Link>
      )
  }
}

/** The alert list, without the queries behind it. `unchecked` names sources that could not be read. */
export function AlertsPanel({
  alerts,
  unchecked,
  checking,
}: {
  alerts: DashboardAlert[]
  unchecked: string[]
  checking: boolean
}) {
  return (
    <Panel
      title="Needs attention"
      note={alerts.length > 0 ? `${alerts.length}` : undefined}
    >
      {alerts.length === 0 ? (
        <p className="text-muted-foreground flex items-center gap-2 px-3 py-3 text-sm" role="status">
          {!checking && unchecked.length === 0 && (
            <CircleCheck className="text-success size-4 shrink-0" aria-hidden="true" />
          )}
          {checking
            ? "Checking certificates, sites, firewall and cron…"
            : unchecked.length > 0
              ? "No problems found in the sources that could be read."
              : "Nothing needs attention."}
        </p>
      ) : (
        <ul className="divide-border divide-y">
          {alerts.map((a) => (
            <li
              key={a.id}
              data-severity={a.severity}
              className={cn(
                "flex items-start justify-between gap-3 px-3 py-2",
                a.severity === "error" && "bg-destructive/5",
              )}
            >
              <div className="flex min-w-0 gap-2">
                {a.severity === "error" ? (
                  <CircleAlert className="text-destructive mt-0.5 size-4 shrink-0" aria-hidden="true" />
                ) : (
                  <TriangleAlert className="text-warning mt-0.5 size-4 shrink-0" aria-hidden="true" />
                )}
                <div className="flex min-w-0 flex-col">
                  <p className="text-sm font-medium">
                    <span className="sr-only">{a.severity === "error" ? "Problem: " : "Warning: "}</span>
                    {a.title}
                  </p>
                  {a.detail && <p className="text-muted-foreground text-xs break-words">{a.detail}</p>}
                  <p className="text-muted-foreground text-xs">{a.source}</p>
                </div>
              </div>
              {a.link && <AlertLinkView link={a.link} />}
            </li>
          ))}
        </ul>
      )}

      {unchecked.length > 0 && (
        <p role="alert" className="text-destructive border-border border-t px-3 py-2 text-xs">
          Could not read: {unchecked.join(", ")}. Alerts from these are missing.
        </p>
      )}
      <p className="text-muted-foreground border-border border-t px-3 py-2 text-xs leading-4">
        Failed background jobs are not listed here: the panel has no job list to read. Failed cron
        runs and failed actions in the audit log are.
      </p>
    </Panel>
  )
}

/** The alert sources, read once so the status line and the list agree. */
function useDashboardAlerts() {
  const certs = useTLSCerts()
  const firewall = useFirewall()
  const sites = useSiteDetails()
  const cron = useCronJobs()
  const audit = useAudit(100)
  const services = useServices()

  const unchecked: string[] = []
  if (certs.error) unchecked.push("certificates")
  if (firewall.error) unchecked.push("firewall")
  if (sites.sites.error || sites.failed) unchecked.push("site files")
  if (cron.error) unchecked.push("cron jobs")
  if (audit.error) unchecked.push("audit log")

  const alerts = deriveAlerts({
    certs: certs.data,
    firewall: firewall.data,
    siteDetails: sites.details,
    cron: cron.data,
    audit: audit.data?.entries,
    services: services.data,
  })

  const checking =
    certs.isPending || firewall.isPending || sites.sites.isPending || cron.isPending || audit.isPending

  return { alerts, unchecked, checking }
}

/* --- Metrics ------------------------------------------------------------ */

interface Series {
  values: number[]
  format: (n: number) => string
  /** Fixed top of the scale (100 for a percentage). */
  ceiling?: number
}

function Tile({
  label,
  value,
  valueClass,
  note,
  noteTitle,
  series,
  sparkLabel,
}: {
  label: string
  value: ReactNode
  valueClass?: string
  note?: ReactNode
  noteTitle?: string
  /** When present the tile opens a detail dialog with a larger chart. */
  series?: Series
  sparkLabel?: string
}) {
  const [open, setOpen] = useState(false)
  const detail = series && series.values.length >= 2 ? series : undefined

  return (
    <section aria-label={label} className="bg-card relative flex min-w-0 flex-col gap-0.5 p-3">
      <h3 className="text-muted-foreground text-xs font-medium">{label}</h3>
      <p
        className={cn("text-xl leading-6 font-semibold tabular-nums", valueClass)}
        data-testid={`tile-${label}`}
      >
        {value}
      </p>
      {note && (
        <p className="text-muted-foreground truncate text-xs" title={noteTitle}>
          {note}
        </p>
      )}
      {series && sparkLabel && (
        <Sparkline
          className="text-primary mt-1.5 h-6 w-full"
          values={series.values}
          ceiling={series.ceiling}
          label={sparkLabel}
        />
      )}

      {detail && (
        <>
          <button
            type="button"
            onClick={() => setOpen(true)}
            aria-label={`${label} details`}
            className="hover:bg-accent/40 focus-visible:ring-ring/50 absolute inset-0 rounded-[inherit] outline-none focus-visible:ring-2"
          />
          <Dialog open={open} onOpenChange={setOpen}>
            <DialogContent className="sm:max-w-xl">
              <DialogHeader>
                <DialogTitle>{label}</DialogTitle>
                <DialogDescription>The last {detail.values.length} samples, oldest on the left.</DialogDescription>
              </DialogHeader>
              <Sparkline
                area
                className="text-primary bg-muted/40 h-44 w-full rounded-md"
                values={detail.values}
                ceiling={detail.ceiling}
                label={`${label} detail chart`}
              />
              <SeriesStats series={detail} />
            </DialogContent>
          </Dialog>
        </>
      )}
    </section>
  )
}

function SeriesStats({ series }: { series: Series }) {
  const { values, format } = series
  const now = values[values.length - 1]
  const avg = values.reduce((a, b) => a + b, 0) / values.length
  const stats: [string, number][] = [
    ["Now", now],
    ["Average", avg],
    ["Peak", Math.max(...values)],
    ["Low", Math.min(...values)],
  ]
  return (
    <dl className="grid grid-cols-4 gap-2">
      {stats.map(([name, v]) => (
        <div key={name} className="bg-muted/50 rounded-md px-2 py-1.5 text-center">
          <dt className="text-muted-foreground text-xs">{name}</dt>
          <dd className="text-sm font-semibold tabular-nums">{format(v)}</dd>
        </div>
      ))}
    </dl>
  )
}

/** Interfaces that carry traffic. Loopback is left out of the sum and the list. */
function externalNics(s: Snapshot) {
  const real = s.net.filter((n) => n.iface !== "lo")
  return real.length > 0 ? real : s.net
}

/** The disk rows. Free space is what runs out, so the bar is use and the text is what is left. */
export function DisksPanel({ disks }: { disks: Snapshot["disks"] }) {
  return (
    <Panel title="Disks" note={disks.length > 0 ? `${disks.length}` : undefined}>
      {disks.length === 0 ? (
        <p className="text-muted-foreground px-3 py-3 text-sm">No mounted disks were reported.</p>
      ) : (
        <ul className="divide-border divide-y">
          {disks.map((d) => (
            <li key={d.mount} className="flex flex-col gap-1.5 px-3 py-2">
              <div className="flex items-baseline justify-between gap-2">
                <span className="font-mono text-xs break-all">{d.mount}</span>
                <span className={cn("text-sm font-medium tabular-nums", levelText(d.used_percent))}>
                  {formatPercent(d.used_percent)}
                </span>
              </div>
              <div
                role="meter"
                aria-label={`Disk use of ${d.mount}`}
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={Math.round(d.used_percent)}
                aria-valuetext={`${formatPercent(d.used_percent)} used`}
                className="bg-muted h-1.5 overflow-hidden rounded-full"
              >
                <div
                  className={cn(
                    "h-full",
                    d.used_percent >= 90 ? "bg-destructive" : d.used_percent >= 75 ? "bg-warning" : "bg-primary",
                  )}
                  style={{ width: `${Math.min(Math.max(d.used_percent, 0), 100)}%` }}
                />
              </div>
              <p className="text-muted-foreground text-xs">
                {formatBytes(d.free_bytes)} free of {formatBytes(d.total_bytes)} · {d.fstype} on {d.device}
              </p>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

/** The metric strip for a series of snapshots. `latest` null means no sample yet. */
export function MetricTiles({
  latest,
  history,
  showDisks = true,
}: {
  latest: Snapshot | null
  history: Snapshot[]
  showDisks?: boolean
}) {
  if (latest === null) {
    return (
      <p className="text-muted-foreground text-sm" role="status">
        Waiting for the first sample.
      </p>
    )
  }

  const measuring = !latest.rates
  const mem = latest.memory
  const memPct = ratioPercent(mem.used_bytes, mem.total_bytes)
  const swapPct = ratioPercent(mem.swap_used_bytes, mem.swap_total_bytes)
  const cores = latest.cpu_cores.length
  const nics = externalNics(latest)
  const rx = nics.reduce((sum, n) => sum + n.rx_bytes_per_sec, 0)
  const tx = nics.reduce((sum, n) => sum + n.tx_bytes_per_sec, 0)
  const n = history.length

  return (
    <div className="flex flex-col gap-3">
      <div className="bg-border grid grid-cols-2 gap-px overflow-hidden rounded-lg border sm:grid-cols-3 xl:grid-cols-6">
        <Tile
          label="CPU"
          value={measuring ? "Measuring…" : formatPercent(latest.cpu_percent)}
          valueClass={measuring ? undefined : levelText(latest.cpu_percent)}
          note={`${cores} ${cores === 1 ? "core" : "cores"}`}
          series={{ values: history.map((s) => s.cpu_percent), format: formatPercent, ceiling: 100 }}
          sparkLabel={`CPU use over the last ${n} samples`}
        />

        <Tile
          label="Load"
          value={latest.load.one.toFixed(2)}
          note={`5m ${latest.load.five.toFixed(2)} · 15m ${latest.load.fifteen.toFixed(2)}`}
          noteTitle={`${latest.load.running} of ${latest.load.total} running`}
          series={{ values: history.map((s) => s.load.one), format: (v) => v.toFixed(2) }}
          sparkLabel={`One-minute load over the last ${n} samples`}
        />

        <Tile
          label="Memory"
          value={formatPercent(memPct)}
          valueClass={levelText(memPct)}
          note={`${formatBytes(mem.used_bytes)} of ${formatBytes(mem.total_bytes)}`}
          noteTitle={`${formatBytes(mem.available_bytes)} available`}
          series={{
            values: history.map((s) => ratioPercent(s.memory.used_bytes, s.memory.total_bytes)),
            format: formatPercent,
            ceiling: 100,
          }}
          sparkLabel={`Memory use over the last ${n} samples`}
        />

        <Tile
          label="Swap"
          value={mem.swap_total_bytes === 0 ? "No swap" : formatPercent(swapPct)}
          valueClass={mem.swap_total_bytes === 0 ? "text-muted-foreground" : levelText(swapPct)}
          note={
            mem.swap_total_bytes === 0
              ? "None configured"
              : `${formatBytes(mem.swap_used_bytes)} of ${formatBytes(mem.swap_total_bytes)}`
          }
        />

        <Tile
          label="Network"
          value={measuring ? "Measuring…" : `↓ ${formatRate(rx)}`}
          note={
            measuring ? undefined : (
              <>
                ↑ {formatRate(tx)}
                {nics.length > 0 && <> · {nics.map((nic) => nic.iface).join(", ")}</>}
              </>
            )
          }
          series={{
            values: history.map((s) =>
              externalNics(s).reduce((sum, nic) => sum + nic.rx_bytes_per_sec + nic.tx_bytes_per_sec, 0),
            ),
            format: formatRate,
          }}
          sparkLabel={`Network traffic over the last ${n} samples`}
        />

        <Tile label="Uptime" value={formatUptime(latest.uptime_seconds)} note="Since the last boot" />
      </div>

      {showDisks && <DisksPanel disks={latest.disks} />}
    </div>
  )
}

function MetricsSection({ live }: { live: LiveSysinfo }) {
  const { latest, history, isPending, error } = live

  if (isPending) {
    return <SkeletonRows label="Loading metrics…" />
  }
  if (error && latest === null) {
    return (
      <p role="alert" className="text-destructive text-sm">
        {errorText(error, "The metrics could not be loaded.")}
      </p>
    )
  }
  return <MetricTiles latest={latest} history={history} showDisks={false} />
}

/* --- Services ----------------------------------------------------------- */

const ACTION_ORDER: ServiceAction[] = ["start", "stop", "restart"]
const ACTION_LABEL: Record<ServiceAction, string> = { start: "Start", stop: "Stop", restart: "Restart" }
const KIND_LABEL: Record<ServiceRow["kind"], string> = {
  systemd: "System service",
  container: "Container",
  app: "App",
}

function stateDot(state: ServiceRow["state"]): string {
  if (state === "failed") return "bg-destructive"
  if (state === "running") return "bg-success"
  return "bg-muted-foreground/50"
}

/** Failed first, then anything not running, then running; by name within each. */
function bySeverity(a: ServiceRow, b: ServiceRow): number {
  const rank = (s: ServiceRow["state"]) => (s === "failed" ? 0 : s === "running" ? 2 : 1)
  return rank(a.state) - rank(b.state) || a.name.localeCompare(b.name)
}

function confirmCopy(row: ServiceRow, action: ServiceAction): ReactNode {
  if (action === "stop") {
    return (
      <p>
        {row.name} stops and stays stopped until you start it again. Anything that depends on it stops
        working meanwhile.
      </p>
    )
  }
  return (
    <>
      <p>{row.name} is unavailable for a few seconds while it restarts.</p>
      {row.name === "panel" && (
        <p>This page loses its connection to the panel for that time and may show errors.</p>
      )}
    </>
  )
}

/** A refusal with the command's own text, which is what tells the operator why. */
export function serviceErrorText(error: unknown, fallback: string): string | null {
  if (!error) return null
  if (error instanceof ApiError) {
    const base = error.detail || fallback
    return error.output ? `${base} ${error.output.trim()}` : base
  }
  return fallback
}

/** The service rows, without the queries behind them. */
export function ServiceGrid({
  services,
  busyName,
  onAct,
}: {
  services: ServiceRow[]
  /** The row whose action is in flight; its buttons are disabled. */
  busyName: string | null
  onAct: (row: ServiceRow, action: ServiceAction) => void
}) {
  if (services.length === 0) {
    return <p className="text-muted-foreground px-3 py-3 text-sm">No services are reported.</p>
  }
  return (
    <ul className="divide-border divide-y">
      {services.map((row) => {
        const actions = ACTION_ORDER.filter((a) => row.actions.includes(a))
        return (
          <li
            key={row.name}
            data-testid="service-card"
            data-service={row.name}
            className="hover:bg-accent/30 grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-0.5 px-3 py-1.5 md:grid-cols-[minmax(0,16.25rem)_minmax(0,1fr)_7rem_13.75rem]"
          >
            <div className="min-w-0">
              <p className="text-sm leading-5 font-medium break-all">{row.name}</p>
              <p className="text-muted-foreground text-xs leading-4">{KIND_LABEL[row.kind] ?? row.kind}</p>
            </div>
            <p
              className="text-muted-foreground order-last col-span-2 min-w-0 truncate text-xs md:order-none md:col-span-1"
              title={row.detail || undefined}
            >
              {row.detail}
            </p>
            <span className="flex items-center gap-1.5 justify-self-end text-xs md:justify-self-start">
              <span className={cn("size-1.5 rounded-full", stateDot(row.state))} aria-hidden="true" />
              {row.state}
            </span>
            <div className="col-start-2 row-start-1 flex justify-end gap-1 md:col-start-4">
              {actions.map((a) => (
                <Button
                  key={a}
                  size="sm"
                  variant="outline"
                  className="h-6 px-2"
                  disabled={busyName === row.name}
                  aria-label={`${ACTION_LABEL[a]} ${row.name}`}
                  onClick={() => onAct(row, a)}
                >
                  {ACTION_LABEL[a]}
                </Button>
              ))}
            </div>
          </li>
        )
      })}
    </ul>
  )
}

type ServiceFilter = "all" | "problems" | "running"

/**
 * The services panel and its confirm dialog. On the dashboard `limit` shows the
 * most urgent few with a link to the full page; the page passes `filters`.
 */
export function ServicesSection({ limit, filters = false }: { limit?: number; filters?: boolean }) {
  const services = useServices()
  const control = useControlService()
  const [confirming, setConfirming] = useState<{ row: ServiceRow; action: ServiceAction } | null>(null)
  const [query, setQuery] = useState("")
  const [filter, setFilter] = useState<ServiceFilter>("all")

  const all = useMemo(() => [...(services.data ?? [])].sort(bySeverity), [services.data])
  const running = all.filter((s) => s.state === "running").length

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase()
    let rows = all
    if (filter === "problems") rows = rows.filter((s) => s.state !== "running")
    if (filter === "running") rows = rows.filter((s) => s.state === "running")
    if (q !== "") rows = rows.filter((s) => `${s.name} ${s.detail ?? ""}`.toLowerCase().includes(q))
    return limit ? rows.slice(0, limit) : rows
  }, [all, query, filter, limit])

  const run = (row: ServiceRow, action: ServiceAction, after?: () => void) =>
    control.mutate({ name: row.name, action }, { onSuccess: after })

  const act = (row: ServiceRow, action: ServiceAction) => {
    control.reset()
    // Start changes nothing that is running; the others take something down.
    if (action === "start") run(row, action)
    else setConfirming({ row, action })
  }

  const filterButtons: { id: ServiceFilter; label: string }[] = [
    { id: "all", label: "All" },
    { id: "problems", label: "Not running" },
    { id: "running", label: "Running" },
  ]

  return (
    <Panel
      title="Services"
      note={services.data ? `${running} of ${all.length} running` : undefined}
      action={
        limit && all.length > limit ? (
          <Link to="/services" className="text-primary flex items-center gap-1 text-xs font-medium hover:underline">
            View all {all.length}
            <ArrowRight className="size-3" aria-hidden="true" />
          </Link>
        ) : undefined
      }
    >
      {filters && (
        <div className="border-border flex flex-wrap items-center gap-2 border-b px-3 py-2">
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Filter by name or detail"
            aria-label="Filter services"
            className="h-7 max-w-64"
          />
          <div role="group" aria-label="State" className="bg-muted flex gap-0.5 rounded-md p-0.5">
            {filterButtons.map((f) => (
              <button
                key={f.id}
                type="button"
                aria-pressed={filter === f.id}
                onClick={() => setFilter(f.id)}
                className={cn(
                  "h-6 rounded px-2 text-xs font-medium",
                  filter === f.id ? "bg-card text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
                )}
              >
                {f.label}
              </button>
            ))}
          </div>
        </div>
      )}

      {services.isPending ? (
        <div className="px-3 py-3"><SkeletonRows label="Loading services…" /></div>
      ) : services.error ? (
        <p role="alert" className="text-destructive px-3 py-3 text-sm">
          {errorText(services.error, "The services could not be loaded.")}
        </p>
      ) : (
        <ServiceGrid
          services={visible}
          busyName={control.isPending ? (control.variables?.name ?? null) : null}
          onAct={act}
        />
      )}

      {confirming === null && control.error && (
        <div className="px-3 pb-3">
          <ApplyError error={control.error} fallback="The action failed." />
        </div>
      )}

      <ConfirmDialog
        open={confirming !== null}
        onOpenChange={(open) => {
          if (!open) {
            setConfirming(null)
            control.reset()
          }
        }}
        title={confirming ? `${ACTION_LABEL[confirming.action]} ${confirming.row.name}?` : ""}
        confirmLabel={confirming ? ACTION_LABEL[confirming.action] : "Confirm"}
        pendingLabel={confirming?.action === "stop" ? "Stopping…" : "Restarting…"}
        pending={control.isPending}
        destructive={confirming?.action === "stop"}
        error={serviceErrorText(control.error, "The action failed.")}
        onConfirm={() => confirming && run(confirming.row, confirming.action, () => setConfirming(null))}
      >
        {confirming && confirmCopy(confirming.row, confirming.action)}
      </ConfirmDialog>
    </Panel>
  )
}
