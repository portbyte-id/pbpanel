import { Link } from "@tanstack/react-router"
import { CircleAlert, TriangleAlert } from "lucide-react"
import { useState, type ReactNode } from "react"

import { ApplyError } from "@/components/ApplyError"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { Sparkline } from "@/components/Sparkline"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
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

function errorText(error: unknown, fallback: string): string {
  return error instanceof ApiError ? error.detail || fallback : fallback
}

/** Health at a glance: what needs attention, the machine's numbers, and the services. */
export function DashboardPage() {
  return (
    <div className="flex max-w-6xl flex-col gap-8">
      <AlertsSection />
      <MetricsSection />
      <ServicesSection />
    </div>
  )
}

/* --- Alerts ------------------------------------------------------------- */

function AlertLinkView({ link }: { link: AlertLink }) {
  const cls = "text-sm font-medium underline underline-offset-2"
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
    <section aria-labelledby="dash-alerts" className="flex flex-col gap-3">
      <h2 id="dash-alerts" className="text-base font-semibold">
        Needs attention
      </h2>

      {alerts.length === 0 ? (
        <p className="text-muted-foreground text-sm" role="status">
          {checking
            ? "Checking certificates, sites, firewall and cron…"
            : unchecked.length > 0
              ? "No problems found in the sources that could be read."
              : "Nothing needs attention."}
        </p>
      ) : (
        <ul className="flex flex-col gap-2">
          {alerts.map((a) => (
            <li
              key={a.id}
              data-severity={a.severity}
              className={
                a.severity === "error"
                  ? "border-destructive/40 bg-destructive/5 flex flex-wrap items-start justify-between gap-3 rounded-lg border p-3"
                  : "flex flex-wrap items-start justify-between gap-3 rounded-lg border border-amber-500/50 bg-amber-500/5 p-3"
              }
            >
              <div className="flex min-w-0 gap-3">
                {a.severity === "error" ? (
                  <CircleAlert className="text-destructive mt-0.5 size-4 shrink-0" aria-hidden="true" />
                ) : (
                  <TriangleAlert className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-amber-400" aria-hidden="true" />
                )}
                <div className="flex min-w-0 flex-col gap-0.5">
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
        <p role="alert" className="text-destructive text-xs">
          Could not read: {unchecked.join(", ")}. Alerts from these are missing.
        </p>
      )}
      <p className="text-muted-foreground text-xs">
        Failed background jobs are not listed here: the panel has no job list to read. Failed cron
        runs and failed actions in the audit log are.
      </p>
    </section>
  )
}

function AlertsSection() {
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

  return <AlertsPanel alerts={alerts} unchecked={unchecked} checking={checking} />
}

/* --- Metrics ------------------------------------------------------------ */

function Tile({
  label,
  value,
  note,
  children,
}: {
  label: string
  value: ReactNode
  note?: ReactNode
  children?: ReactNode
}) {
  return (
    <section aria-label={label} className="border-border flex flex-col gap-1 rounded-lg border p-4">
      <h3 className="text-muted-foreground text-xs font-medium">{label}</h3>
      <p className="text-2xl font-semibold tabular-nums" data-testid={`tile-${label}`}>
        {value}
      </p>
      {note && <p className="text-muted-foreground text-xs">{note}</p>}
      {children}
    </section>
  )
}

const sparkClass = "text-primary mt-1 h-8 w-full"

/** Interfaces that carry traffic. Loopback is left out of the sum and the list. */
function externalNics(s: Snapshot) {
  const real = s.net.filter((n) => n.iface !== "lo")
  return real.length > 0 ? real : s.net
}

/** The tiles and disks for a series of snapshots. `latest` null means no sample yet. */
export function MetricTiles({
  latest,
  history,
}: {
  latest: Snapshot | null
  history: Snapshot[]
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
  const netSeries = history.map((s) =>
    externalNics(s).reduce((sum, n) => sum + n.rx_bytes_per_sec + n.tx_bytes_per_sec, 0),
  )

  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <Tile
          label="CPU"
          value={measuring ? "Measuring…" : formatPercent(latest.cpu_percent)}
          note={`${cores} ${cores === 1 ? "core" : "cores"}`}
        >
          <Sparkline
            className={sparkClass}
            values={history.map((s) => s.cpu_percent)}
            ceiling={100}
            label={`CPU use over the last ${history.length} samples`}
          />
        </Tile>

        <Tile
          label="Load"
          value={latest.load.one.toFixed(2)}
          note={`5 min ${latest.load.five.toFixed(2)} · 15 min ${latest.load.fifteen.toFixed(2)} · ${latest.load.running} of ${latest.load.total} running`}
        >
          <Sparkline
            className={sparkClass}
            values={history.map((s) => s.load.one)}
            label={`One-minute load over the last ${history.length} samples`}
          />
        </Tile>

        <Tile
          label="Memory"
          value={formatPercent(memPct)}
          note={`${formatBytes(mem.used_bytes)} of ${formatBytes(mem.total_bytes)} used, ${formatBytes(mem.available_bytes)} available`}
        >
          <Sparkline
            className={sparkClass}
            values={history.map((s) => ratioPercent(s.memory.used_bytes, s.memory.total_bytes))}
            ceiling={100}
            label={`Memory use over the last ${history.length} samples`}
          />
        </Tile>

        <Tile
          label="Swap"
          value={mem.swap_total_bytes === 0 ? "No swap" : formatPercent(swapPct)}
          note={
            mem.swap_total_bytes === 0
              ? "This server has no swap configured."
              : `${formatBytes(mem.swap_used_bytes)} of ${formatBytes(mem.swap_total_bytes)} used`
          }
        />

        <Tile
          label="Network"
          value={measuring ? "Measuring…" : `↓ ${formatRate(rx)}`}
          note={
            measuring ? undefined : (
              <>
                ↑ {formatRate(tx)}
                {nics.length > 0 && <> · {nics.map((n) => n.iface).join(", ")}</>}
              </>
            )
          }
        >
          <Sparkline
            className={sparkClass}
            values={netSeries}
            label={`Network traffic over the last ${history.length} samples`}
          />
        </Tile>

        <Tile label="Uptime" value={formatUptime(latest.uptime_seconds)} note="Since the last boot" />
      </div>

      <div className="flex flex-col gap-2">
        <h3 className="text-sm font-semibold">Disks</h3>
        {latest.disks.length === 0 ? (
          <p className="text-muted-foreground text-sm">No mounted disks were reported.</p>
        ) : (
          <ul className="grid grid-cols-1 gap-3 md:grid-cols-2">
            {latest.disks.map((d) => (
              <li key={d.mount} className="border-border flex flex-col gap-1.5 rounded-lg border p-3">
                <div className="flex items-baseline justify-between gap-2">
                  <span className="font-mono text-sm break-all">{d.mount}</span>
                  <span className="text-sm font-medium tabular-nums">{formatPercent(d.used_percent)}</span>
                </div>
                <div
                  role="meter"
                  aria-label={`Disk use of ${d.mount}`}
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={Math.round(d.used_percent)}
                  aria-valuetext={`${formatPercent(d.used_percent)} used`}
                  className="bg-muted h-2 overflow-hidden rounded-full"
                >
                  <div
                    className={d.used_percent >= 90 ? "bg-destructive h-full" : "bg-primary h-full"}
                    style={{ width: `${Math.min(Math.max(d.used_percent, 0), 100)}%` }}
                  />
                </div>
                <p className="text-muted-foreground text-xs">
                  {formatBytes(d.used_bytes)} of {formatBytes(d.total_bytes)} · {formatBytes(d.free_bytes)} free ·{" "}
                  {d.fstype} on {d.device}
                </p>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}

function streamNote(status: SysinfoStreamStatus, seconds: number): string {
  if (status === "live") return "Live."
  if (status === "polling") return `Live stream unavailable. Refreshing every ${seconds} seconds.`
  return "Connecting…"
}

function MetricsSection() {
  const { latest, history, status, intervalSeconds, isPending, error } = useSysinfoLive()

  return (
    <section aria-labelledby="dash-metrics" className="flex flex-col gap-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 id="dash-metrics" className="text-base font-semibold">
          This server
        </h2>
        <p className="text-muted-foreground text-xs" aria-live="polite">
          {streamNote(status, intervalSeconds)}
        </p>
      </div>
      {isPending ? (
        <p className="text-muted-foreground text-sm">Loading metrics…</p>
      ) : error && latest === null ? (
        <p role="alert" className="text-destructive text-sm">
          {errorText(error, "The metrics could not be loaded.")}
        </p>
      ) : (
        <MetricTiles latest={latest} history={history} />
      )}
    </section>
  )
}

/* --- Services ----------------------------------------------------------- */

const ACTION_ORDER: ServiceAction[] = ["start", "stop", "restart"]
const ACTION_LABEL: Record<ServiceAction, string> = { start: "Start", stop: "Stop", restart: "Restart" }
const KIND_LABEL: Record<ServiceRow["kind"], string> = {
  systemd: "System service",
  container: "Container",
  app: "App",
}

function stateVariant(state: ServiceRow["state"]) {
  if (state === "failed") return "destructive" as const
  if (state === "running") return "secondary" as const
  return "outline" as const
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

/** The service cards, without the queries behind them. */
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
    return <p className="text-muted-foreground text-sm">No services are reported.</p>
  }
  return (
    <ul className="grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3">
      {services.map((row) => {
        const actions = ACTION_ORDER.filter((a) => row.actions.includes(a))
        return (
          <li
            key={row.name}
            data-testid="service-card"
            data-service={row.name}
            className="border-border flex flex-col gap-2 rounded-lg border p-3"
          >
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <p className="text-sm font-medium break-all">{row.name}</p>
                <p className="text-muted-foreground text-xs">{KIND_LABEL[row.kind] ?? row.kind}</p>
              </div>
              <Badge variant={stateVariant(row.state)}>{row.state}</Badge>
            </div>
            {row.detail && <p className="text-muted-foreground text-xs break-words">{row.detail}</p>}
            {actions.length > 0 && (
              <div className="mt-auto flex flex-wrap gap-1.5">
                {actions.map((a) => (
                  <Button
                    key={a}
                    size="sm"
                    variant="outline"
                    disabled={busyName === row.name}
                    aria-label={`${ACTION_LABEL[a]} ${row.name}`}
                    onClick={() => onAct(row, a)}
                  >
                    {ACTION_LABEL[a]}
                  </Button>
                ))}
              </div>
            )}
          </li>
        )
      })}
    </ul>
  )
}

function ServicesSection() {
  const services = useServices()
  const control = useControlService()
  const [confirming, setConfirming] = useState<{ row: ServiceRow; action: ServiceAction } | null>(null)

  const run = (row: ServiceRow, action: ServiceAction, after?: () => void) =>
    control.mutate({ name: row.name, action }, { onSuccess: after })

  const act = (row: ServiceRow, action: ServiceAction) => {
    control.reset()
    // Start changes nothing that is running; the others take something down.
    if (action === "start") run(row, action)
    else setConfirming({ row, action })
  }

  return (
    <section aria-labelledby="dash-services" className="flex flex-col gap-3">
      <h2 id="dash-services" className="text-base font-semibold">
        Services
      </h2>

      {services.isPending ? (
        <p className="text-muted-foreground text-sm">Loading services…</p>
      ) : services.error ? (
        <p role="alert" className="text-destructive text-sm">
          {errorText(services.error, "The services could not be loaded.")}
        </p>
      ) : (
        <ServiceGrid
          services={services.data}
          busyName={control.isPending ? (control.variables?.name ?? null) : null}
          onAct={act}
        />
      )}

      {confirming === null && control.error && (
        <ApplyError error={control.error} fallback="The action failed." />
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
    </section>
  )
}
