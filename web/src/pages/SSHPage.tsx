import { Download, Pause, Play, ShieldAlert } from "lucide-react"
import { useId, useMemo, useState } from "react"

import { ErrorState } from "@/components/ErrorState"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Callout } from "@/components/ui/callout"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select } from "@/components/ui/select"
import { SkeletonRows } from "@/components/ui/skeleton"
import { api, ApiError, type SSHAttempt, type SSHSummary } from "@/lib/api"
import { formatWhen } from "@/lib/cron"
import { useDebounced, useSSHAttempts } from "@/lib/queries"

/** The windows offered. Every value is a valid `range`: 5m is the floor, 720h the cap. */
const RANGES: { value: string; label: string }[] = [
  { value: "1h", label: "Last hour" },
  { value: "24h", label: "Last 24 hours" },
  { value: "168h", label: "Last 7 days" },
  { value: "720h", label: "Last 30 days" },
]

const OUTCOMES: { value: "" | "failed" | "success"; label: string }[] = [
  { value: "", label: "All attempts" },
  { value: "failed", label: "Failed only" },
  { value: "success", label: "Succeeded only" },
]

/** The page asks again at this interval while it is open; a journal is not a hot source. */
const POLL_MS = 15_000

const METHOD_LABEL: Record<SSHAttempt["method"], string> = {
  password: "Password",
  publickey: "Public key",
  none: "No method",
}

/**
 * sshauth answers 503 `unavailable` when the host has no readable sshd journal.
 * That is a fact about the host, not a failed request: the page says so and
 * offers no retry that would only fail again.
 */
export function isSSHUnavailable(error: unknown): error is ApiError {
  return error instanceof ApiError && error.status === 503 && error.code === "unavailable"
}

/** SSH login attempts: the summary, the filters and the newest-first feed. */
export function SSHPage() {
  const id = useId()
  const [range, setRange] = useState("24h")
  const [outcome, setOutcome] = useState<"" | "failed" | "success">("")
  const [user, setUser] = useState("")
  const [ip, setIp] = useState("")
  const [paused, setPaused] = useState(false)

  // Only the window and the outcome go to the server. The two text boxes filter
  // the page it already returned, so typing never re-reads the journal.
  const dUser = useDebounced(user.trim().toLowerCase(), 250)
  const dIp = useDebounced(ip.trim().toLowerCase(), 250)

  const query = useSSHAttempts({ range, outcome, limit: 200 }, paused ? false : POLL_MS)
  const report = query.data

  const rows = useMemo(() => {
    const all = report?.attempts ?? []
    return all.filter(
      (a) => (dUser === "" || a.user.toLowerCase().includes(dUser)) && (dIp === "" || a.ip.includes(dIp)),
    )
  }, [report, dUser, dIp])

  const unavailable = isSSHUnavailable(query.error)

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-end gap-4">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-range`}>Period</Label>
          <Select id={`${id}-range`} className="w-48" value={range} onChange={(e) => setRange(e.target.value)}>
            {RANGES.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </Select>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-outcome`}>Outcome</Label>
          <Select
            id={`${id}-outcome`}
            className="w-48"
            value={outcome}
            onChange={(e) => setOutcome(e.target.value as "" | "failed" | "success")}
          >
            {OUTCOMES.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </Select>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-user`}>Username contains</Label>
          <Input
            id={`${id}-user`}
            className="w-44 font-mono"
            value={user}
            onChange={(e) => setUser(e.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-ip`}>IP contains</Label>
          <Input
            id={`${id}-ip`}
            className="w-44 font-mono"
            value={ip}
            onChange={(e) => setIp(e.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </div>
        <Button
          type="button"
          variant="outline"
          aria-pressed={paused}
          disabled={unavailable}
          onClick={() => setPaused((p) => !p)}
        >
          {paused ? <Play aria-hidden="true" /> : <Pause aria-hidden="true" />}
          {paused ? "Resume" : "Pause"}
        </Button>
        {!unavailable && (
          <Button asChild variant="outline">
            <a href={api.sshAbuseIPDBUrl(range)} download>
              <Download aria-hidden="true" />
              Download for AbuseIPDB
            </a>
          </Button>
        )}
      </div>

      {query.isPending ? (
        <SkeletonRows label="Loading SSH attempts…" />
      ) : unavailable ? (
        <UnavailableState error={query.error as ApiError} />
      ) : query.error ? (
        <ErrorState
          error={query.error}
          fallback="The SSH login-attempt feed could not be loaded."
          onRetry={() => void query.refetch()}
        />
      ) : report ? (
        <>
          <SummaryStrip summary={report.summary} />
          <AttemptTable rows={rows} total={report.total} fetched={report.attempts.length} paused={paused} />
        </>
      ) : null}
    </div>
  )
}

function UnavailableState({ error }: { error: ApiError }) {
  return (
    <Callout tone="warning" role="alert" className="flex gap-3">
      <ShieldAlert className="text-warning mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <div className="flex flex-col gap-1">
        <p className="text-sm font-medium">The SSH monitor is not available on this host.</p>
        <p className="text-muted-foreground text-sm">
          The sshd journal cannot be read here. This is expected on a host with no sshd, or one whose journal this
          process cannot open — it is not a problem with this page.
        </p>
        {error.detail && <p className="text-muted-foreground text-xs break-words">{error.detail}</p>}
      </div>
    </Callout>
  )
}

function Tile({ label, value, tone }: { label: string; value: string; tone?: "destructive" }) {
  return (
    <div className="bg-card flex flex-col gap-1 rounded-lg border p-3">
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className={"text-2xl font-semibold tabular-nums " + (tone === "destructive" ? "text-destructive" : "")}>
        {value}
      </dd>
    </div>
  )
}

/** The figures the response carries: totals, distinct counts and the two top lists. */
function SummaryStrip({ summary }: { summary: SSHSummary }) {
  return (
    <section aria-label="Summary for the period" className="flex flex-col gap-4">
      <dl className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Tile
          label="Failed attempts"
          value={summary.failed.toLocaleString("en-US")}
          tone={summary.failed > 0 ? "destructive" : undefined}
        />
        <Tile label="Succeeded" value={summary.succeeded.toLocaleString("en-US")} />
        <Tile label="Distinct IPs" value={summary.distinct_ips.toLocaleString("en-US")} />
        <Tile label="Distinct users" value={summary.distinct_users.toLocaleString("en-US")} />
      </dl>

      <div className="grid gap-4 md:grid-cols-2">
        <TopList
          title="Top IPs by failures"
          firstHeading="IP"
          rows={summary.top_ips.map((r) => ({ key: r.ip, label: r.ip, failed: r.failed, total: r.total }))}
        />
        <TopList
          title="Top users by failures"
          firstHeading="User"
          rows={summary.top_users.map((r) => ({ key: r.user, label: r.user, failed: r.failed, total: r.total }))}
        />
      </div>
    </section>
  )
}

function TopList({
  title,
  firstHeading,
  rows,
}: {
  title: string
  firstHeading: string
  rows: { key: string; label: string; failed: number; total: number }[]
}) {
  return (
    <section aria-label={title} className="flex min-w-0 flex-col gap-1.5">
      <h2 className="text-sm font-semibold">{title}</h2>
      {rows.length === 0 ? (
        <p className="text-muted-foreground text-sm">Nothing in this period.</p>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-sm">
            <caption className="sr-only">{title}</caption>
            <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
              <tr>
                <th scope="col" className="px-3 py-1.5 font-medium">{firstHeading}</th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">Failed</th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">Total</th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {rows.map((r) => (
                <tr key={r.key}>
                  <td className="px-3 py-1.5 font-mono text-xs break-all">{r.label || "—"}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{r.failed.toLocaleString("en-US")}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{r.total.toLocaleString("en-US")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

function AttemptTable({
  rows,
  total,
  fetched,
  paused,
}: {
  rows: SSHAttempt[]
  total: number
  fetched: number
  paused: boolean
}) {
  const truncated = fetched < total
  return (
    <section aria-label="Login attempts" className="flex flex-col gap-2">
      <p className="text-muted-foreground text-xs" aria-live="polite">
        Showing {rows.length.toLocaleString("en-US")} of {total.toLocaleString("en-US")} attempts
        {truncated ? ` (the newest ${fetched.toLocaleString("en-US")} are loaded)` : ""}.{" "}
        {paused ? "Paused — resume to catch up." : "Newest first."}
      </p>

      {rows.length === 0 ? (
        <p className="text-muted-foreground border-border rounded-md border border-dashed p-6 text-sm" role="status">
          No attempts match. Widen the period or clear a filter.
        </p>
      ) : (
        <div
          className="focus-visible:ring-ring relative max-h-[36rem] overflow-auto rounded-lg border focus-visible:ring-2 focus-visible:outline-none"
          tabIndex={0}
        >
          <table className="w-full text-sm">
            <caption className="sr-only">SSH login attempts, newest first</caption>
            <thead className="bg-muted text-muted-foreground sticky top-0 text-left text-xs">
              <tr>
                <th scope="col" className="px-3 py-1.5 font-medium">Time</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Outcome</th>
                <th scope="col" className="px-3 py-1.5 font-medium">IP</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Username</th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">Port</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Method</th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {rows.map((a, i) => (
                <tr key={`${a.at}-${a.ip}-${a.port}-${i}`}>
                  <td className="px-3 py-1 text-xs whitespace-nowrap tabular-nums">
                    <time dateTime={a.at} title={a.at}>
                      {formatWhen(a.at)}
                    </time>
                  </td>
                  <td className="px-3 py-1">
                    <Badge variant={a.outcome === "failed" ? "destructive" : "success"}>
                      {a.outcome === "failed" ? "Failed" : "Succeeded"}
                    </Badge>
                  </td>
                  <td className="px-3 py-1 font-mono text-xs">{a.ip}</td>
                  <td className="px-3 py-1 font-mono text-xs break-all">{a.user || "—"}</td>
                  <td className="px-3 py-1 text-right text-xs tabular-nums">{a.port}</td>
                  <td className="px-3 py-1 text-xs">{METHOD_LABEL[a.method]}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}
