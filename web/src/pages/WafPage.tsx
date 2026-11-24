import { Pause, Play } from "lucide-react"
import { useEffect, useId, useMemo, useState } from "react"

import { ErrorState } from "@/components/ErrorState"
import { Sparkline } from "@/components/Sparkline"
import { TrafficChart } from "@/components/TrafficChart"
import { FirewallActivity, percent, PostureBar, ResponseMix } from "@/components/TrafficPanels"
import { BlockPagePanel } from "@/components/WafBlockPage"
import { ProtectionPanel } from "@/components/WafProtection"
import { SourcesPanel } from "@/components/WafSources"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select } from "@/components/ui/select"
import { SkeletonRows } from "@/components/ui/skeleton"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { TableFilter, TablePagination, TableToolbar, usePagedRows } from "@/components/ui/table-controls"
import { useSites } from "@/lib/queries"
import { matchesQuery } from "@/lib/table"
import { formatBytes } from "@/lib/sysinfo"
import {
  compactCount,
  MODE_LABEL,
  statusClass,
  toMillis,
  type TrafficRange,
  type TrafficRequest,
  type TrafficSummary,
  type WafEventSummary,
  type WafMode,
  type WafOverviewSite,
} from "@/lib/waf"
import { useRequestFeed, useWafEventSummary, useWafOverview, useWafTraffic } from "@/lib/wafQueries"

const RANGES: { value: TrafficRange; label: string }[] = [
  { value: "5m", label: "Last 5 minutes" },
  { value: "15m", label: "Last 15 minutes" },
  { value: "1h", label: "Last hour" },
]

/** The same value after it stops changing for `ms`, so typing does not ask the server per key. */
function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

export function statusVariant(status: number): "success" | "info" | "warning" | "destructive" | "outline" {
  switch (statusClass(status)) {
    case "2xx":
      return "success"
    case "3xx":
      return "info"
    case "4xx":
      return "warning"
    case "5xx":
      return "destructive"
    default:
      return "outline"
  }
}

/**
 * The WAF: live traffic, where the requests come from, the firewall events, and
 * the rules that apply to every site, with the page a refused visitor sees.
 */
export function WafPage() {
  const id = useId()
  const sites = useSites()
  const overview = useWafOverview()
  const [site, setSite] = useState("")
  const [range, setRange] = useState<TrafficRange>("15m")
  const traffic = useWafTraffic(site, range)
  const events = useWafEventSummary(site, range)

  const modes = useMemo(
    () => new Map((overview.data?.sites ?? []).map((s) => [s.domain, s])),
    [overview.data],
  )

  return (
    // `relative` contains the absolutely-positioned `sr-only` table captions, so
    // they cannot escape `main`'s clip and grow the document past the viewport.
    <div className="relative flex flex-col gap-6">
      <div className="flex flex-wrap items-end gap-4">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-site`}>Site</Label>
          <Select id={`${id}-site`} className="w-64" value={site} onChange={(e) => setSite(e.target.value)}>
            <option value="">All sites</option>
            {(sites.data ?? []).map((s) => (
              <option key={s.domain} value={s.domain}>
                {s.domain}
              </option>
            ))}
          </Select>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-range`}>Period</Label>
          <Select
            id={`${id}-range`}
            className="w-48"
            value={range}
            onChange={(e) => setRange(e.target.value as TrafficRange)}
          >
            {RANGES.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </Select>
        </div>
        <p className="text-muted-foreground pb-1.5 text-xs" aria-live="polite">
          {traffic.isFetching && !traffic.isPending ? "Updating…" : "Refreshes every 2 seconds while this tab is open."}
        </p>
      </div>

      {overview.data && <PostureBar overview={overview.data} site={site} />}

      {traffic.isPending ? (
        <SkeletonRows label="Loading traffic…" />
      ) : traffic.error ? (
        <ErrorState
          error={traffic.error}
          fallback="The traffic monitor is not available."
          onRetry={() => void traffic.refetch()}
        />
      ) : (
        <Totals data={traffic.data} events={events.data} />
      )}

      <Tabs defaultValue="overview">
        <TabsList aria-label="WAF views">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="top">Top lists</TabsTrigger>
          <TabsTrigger value="firewall">Firewall</TabsTrigger>
          <TabsTrigger value="live">Live requests</TabsTrigger>
          <TabsTrigger value="protection">Protection</TabsTrigger>
          <TabsTrigger value="block-page">Block page</TabsTrigger>
        </TabsList>
        <TabsContent value="overview" className="pt-2">
          {traffic.data && !traffic.error && (
            <Overview data={traffic.data} site={site} range={range} modes={modes} onPickSite={setSite} />
          )}
        </TabsContent>
        <TabsContent value="top" className="pt-2">
          {traffic.data && !traffic.error && <TopLists data={traffic.data} />}
        </TabsContent>
        <TabsContent value="firewall" className="pt-2">
          <FirewallActivity summary={events.data} failed={events.isError} pending={events.isPending} />
        </TabsContent>
        <TabsContent value="live" className="pt-2">
          <RequestFeed site={site} />
        </TabsContent>
        <TabsContent value="protection" className="pt-2">
          <ProtectionPanel />
        </TabsContent>
        <TabsContent value="block-page" className="pt-2">
          <BlockPagePanel />
        </TabsContent>
      </Tabs>
    </div>
  )
}

function Tile({
  label,
  value,
  hint,
  spark,
  tone,
}: {
  label: string
  value: string
  hint?: string
  spark?: number[]
  tone?: "warning" | "destructive"
}) {
  return (
    <div className="bg-card flex flex-col gap-1 rounded-lg border p-3">
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className="flex items-end justify-between gap-2">
        <span
          className={
            "text-2xl font-semibold tabular-nums " +
            (tone === "destructive" ? "text-destructive" : tone === "warning" ? "text-warning" : "")
          }
        >
          {value}
        </span>
        {spark && (
          <Sparkline values={spark} label={`${label} over the period`} className="text-muted-foreground h-6 w-20" />
        )}
      </dd>
      {hint && <p className="text-muted-foreground text-xs">{hint}</p>}
    </div>
  )
}

/** The headline numbers, shown above the tabs for every view. */
function Totals({ data, events }: { data: TrafficSummary; events: WafEventSummary | undefined }) {
  const t = data.totals
  const series = data.series
  const errors = t.s4xx + t.s5xx
  return (
    <dl className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
        <Tile label="Requests per second now" value={data.rps_now.toFixed(1)} />
        <Tile
          label="Requests"
          value={compactCount(t.requests)}
          hint={formatBytes(t.bytes)}
          spark={series.map((p) => p.requests)}
        />
        <Tile
          label="Error rate"
          value={percent(errors, t.requests)}
          hint={`${t.s4xx.toLocaleString("en-US")} 4xx · ${t.s5xx.toLocaleString("en-US")} 5xx`}
          tone={t.s5xx > 0 ? "destructive" : t.s4xx > 0 ? "warning" : undefined}
          spark={series.map((p) => p.s4xx + p.s5xx)}
        />
        <Tile
          label="WAF events"
          value={compactCount(t.waf_events)}
          hint={t.requests > 0 ? `${percent(t.waf_events, t.requests)} of requests` : undefined}
          spark={series.map((p) => p.waf_events)}
        />
        <Tile
          label="Blocked by the firewall"
          value={events ? compactCount(events.by_action.blocked) : "—"}
          hint={events ? `${events.by_action.detected.toLocaleString("en-US")} detected only` : undefined}
          tone={events && events.by_action.blocked > 0 ? "destructive" : undefined}
        />
        <Tile
          label="Average latency"
          value={`${Math.round(t.avg_ms)} ms`}
          hint={`slowest ${Math.round(t.max_ms)} ms`}
          spark={series.map((p) => p.avg_ms)}
        />
    </dl>
  )
}

/** The chart, the response mix and the per-site table: how the traffic moved and where it went. */
function Overview({
  data,
  site,
  range,
  modes,
  onPickSite,
}: {
  data: TrafficSummary
  site: string
  range: TrafficRange
  modes: Map<string, WafOverviewSite>
  onPickSite: (domain: string) => void
}) {
  const t = data.totals
  const series = data.series
  return (
    <div className="flex flex-col gap-6">
      <section aria-label="Requests over time" className="bg-card rounded-lg border p-3">
        <h2 className="mb-2 text-sm font-semibold">Requests over time</h2>
        <TrafficChart series={series} stepSeconds={data.step_seconds} />
      </section>

      <ResponseMix totals={t} />

      {site === "" && <SourcesPanel range={range} />}

      {site === "" && (
        <section aria-label="Sites" className="flex flex-col gap-2">
          <h2 className="text-sm font-semibold">Sites</h2>
          {data.sites.length === 0 ? (
            <p className="text-muted-foreground text-sm">No site has had traffic in this period.</p>
          ) : (
            <SitesTable sites={data.sites} modes={modes} onPickSite={onPickSite} />
          )}
        </section>
      )}
    </div>
  )
}

const NO_WAF_MODE = "none"

/** Per-site traffic with search by domain, a ModSecurity filter, and paging. */
function SitesTable({
  sites,
  modes,
  onPickSite,
}: {
  sites: TrafficSummary["sites"]
  modes: Map<string, WafOverviewSite>
  onPickSite: (domain: string) => void
}) {
  const [search, setSearch] = useState("")
  const [mode, setMode] = useState("all")
  const matching = sites.filter((s) => {
    const current = modes.get(s.domain)?.mode ?? NO_WAF_MODE
    return (mode === "all" || current === mode) && matchesQuery([s.domain], search)
  })
  const pager = usePagedRows(matching, `${search}|${mode}`)
  return (
    <>
      <TableToolbar
        search={search}
        onSearchChange={setSearch}
        searchLabel="Search sites"
        placeholder="Search by domain"
      >
        <TableFilter
          label="ModSecurity"
          value={mode}
          onChange={setMode}
          options={[
            ...(["block", "detect", "off"] as WafMode[]).map((k) => ({ value: k, label: MODE_LABEL[k] })),
            { value: NO_WAF_MODE, label: "No WAF data" },
          ]}
        />
      </TableToolbar>
      {matching.length === 0 ? (
        <p className="text-muted-foreground text-sm">No site matches the filters.</p>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-sm">
            <caption className="sr-only">Traffic per site</caption>
            <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
              <tr>
                <th scope="col" className="px-3 py-1.5 font-medium">Site</th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">Requests</th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">Errors</th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">Error rate</th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">WAF events</th>
                <th scope="col" className="px-3 py-1.5 font-medium">ModSecurity</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Also on</th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {pager.rows.map((s) => {
                const m = modes.get(s.domain)
                return (
                  <tr key={s.domain}>
                    <td className="px-3 py-1.5">
                      <button
                        type="button"
                        onClick={() => onPickSite(s.domain)}
                        className="focus-visible:ring-ring/50 rounded font-medium underline-offset-4 outline-none hover:underline focus-visible:ring-[3px]"
                      >
                        {s.domain}
                      </button>
                    </td>
                    <td className="px-3 py-1.5 text-right tabular-nums">{s.requests.toLocaleString("en-US")}</td>
                    <td className="px-3 py-1.5 text-right tabular-nums">{s.errors.toLocaleString("en-US")}</td>
                    <td className="text-muted-foreground px-3 py-1.5 text-right text-xs tabular-nums">
                      {percent(s.errors, s.requests)}
                    </td>
                    <td className="px-3 py-1.5 text-right tabular-nums">{s.waf_events.toLocaleString("en-US")}</td>
                    <td className="px-3 py-1.5">
                      {m ? (
                        <Badge variant={m.mode === "block" ? "success" : m.mode === "detect" ? "info" : "destructive"}>
                          {MODE_LABEL[m.mode as WafMode]}
                          {m.mode !== "off" && ` · level ${m.paranoia}`}
                        </Badge>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </td>
                    <td className="px-3 py-1.5">
                      <span className="flex flex-wrap gap-1">
                        {m?.rate_limit && <Badge variant="outline">Rate limit</Badge>}
                        {m?.challenge && <Badge variant="outline">Challenge</Badge>}
                        {m && !m.rate_limit && !m.challenge && <span className="text-muted-foreground">—</span>}
                      </span>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      <TablePagination pager={pager} />
    </>
  )
}

/** The busiest clients, paths, status codes and user agents for the period. */
function TopLists({ data }: { data: TrafficSummary }) {
  return (
    <div className="grid gap-6 lg:grid-cols-2">
        <TopTable
          title="Top client IPs"
          head={["IP", "Requests", "Errors", "Sent"]}
          rows={data.top_ips.map((r) => [r.ip, r.requests, r.errors, formatBytes(r.bytes)])}
          mono
        />
        <TopTable
          title="Top paths"
          head={["Path", "Requests", "Errors"]}
          rows={data.top_paths.map((r) => [r.path, r.requests, r.errors])}
          mono
        />
        <TopTable
          title="Status codes"
          head={["Status", "Requests"]}
          rows={data.top_statuses.map((r) => [String(r.status), r.requests])}
          mono
        />
        <TopTable
          title="User agents"
          head={["Agent", "Requests"]}
          rows={data.top_agents.map((r) => [r.agent || "(none)", r.requests])}
        />
    </div>
  )
}

function TopTable({
  title,
  head,
  rows,
  mono,
}: {
  title: string
  head: string[]
  rows: (string | number)[][]
  mono?: boolean
}) {
  const [search, setSearch] = useState("")
  const peak = Math.max(1, ...rows.map((r) => (typeof r[1] === "number" ? r[1] : 0)))
  const matching = rows.filter((r) => matchesQuery([r[0]], search))
  const pager = usePagedRows(matching, search)
  return (
    <section aria-label={title} className="flex min-w-0 flex-col gap-2">
      <h2 className="text-sm font-semibold">{title}</h2>
      {rows.length === 0 ? (
        <p className="text-muted-foreground text-sm">Nothing in this period.</p>
      ) : (
        <>
          <TableToolbar search={search} onSearchChange={setSearch} searchLabel={`Search ${title.toLowerCase()}`} />
          {matching.length === 0 ? (
            <p className="text-muted-foreground text-sm">No rows match the filters.</p>
          ) : (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-sm">
            <caption className="sr-only">{title}</caption>
            <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
              <tr>
                {head.map((h, i) => (
                  <th key={h} scope="col" className={"px-3 py-1.5 font-medium " + (i > 0 ? "text-right" : "")}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {pager.rows.map((r) => (
                <tr key={String(r[0])}>
                  {r.map((cell, i) => (
                    <td
                      key={i}
                      className={
                        "px-3 py-1.5 " +
                        (i === 0
                          ? `${mono ? "font-mono text-xs" : "text-xs"} max-w-[28rem] break-all`
                          : "text-right tabular-nums")
                      }
                    >
                      {typeof cell === "number" ? cell.toLocaleString("en-US") : cell}
                      {i === 0 && typeof r[1] === "number" && (
                        <div aria-hidden="true" className="bg-muted mt-1 h-0.5 rounded-full">
                          <div
                            className="bg-primary/60 h-0.5 rounded-full"
                            style={{ width: `${(Number(r[1]) / peak) * 100}%` }}
                          />
                        </div>
                      )}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
          )}
          <TablePagination pager={pager} />
        </>
      )}
    </section>
  )
}

/* --- Live feed -------------------------------------------------------------- */

const STATUS_FILTERS = [
  { value: "", label: "All statuses" },
  { value: "2xx", label: "2xx" },
  { value: "3xx", label: "3xx" },
  { value: "4xx", label: "4xx" },
  { value: "5xx", label: "5xx" },
  { value: "404", label: "404 only" },
]

function FeedRow({ r, showSite }: { r: TrafficRequest; showSite: boolean }) {
  return (
    <tr>
      <td className="px-3 py-1 text-xs whitespace-nowrap tabular-nums">
        {new Date(toMillis(r.time)).toLocaleTimeString()}
      </td>
      <td className="px-3 py-1">
        <Badge variant={statusVariant(r.status)}>{r.status}</Badge>
      </td>
      <td className="px-3 py-1 font-mono text-xs">{r.method}</td>
      <td className="max-w-[24rem] px-3 py-1 font-mono text-xs break-all" title={r.agent}>
        {r.path}
      </td>
      {showSite && <td className="px-3 py-1 text-xs">{r.site}</td>}
      <td className="px-3 py-1 font-mono text-xs">{r.ip}</td>
      <td className="px-3 py-1 text-right text-xs tabular-nums">{Math.round(r.ms)} ms</td>
      <td className="px-3 py-1 text-right text-xs whitespace-nowrap tabular-nums">{formatBytes(r.bytes)}</td>
    </tr>
  )
}

export function RequestFeed({ site }: { site: string }) {
  const id = useId()
  const [status, setStatus] = useState("")
  const [ip, setIp] = useState("")
  const [text, setText] = useState("")
  const [paused, setPaused] = useState(false)
  const dIp = useDebounced(ip.trim(), 300)
  const dText = useDebounced(text.trim(), 300)

  const filters = useMemo(() => ({ site, status, ip: dIp, q: dText }), [site, status, dIp, dText])
  const { rows, query } = useRequestFeed(filters, paused)
  const pager = usePagedRows(rows, `${site}|${status}|${dIp}|${dText}`)

  return (
    <section aria-labelledby={`${id}-heading`} className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end gap-3">
        <h2 id={`${id}-heading`} className="mr-auto text-sm font-semibold">
          Live requests
        </h2>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-status`}>Status</Label>
          <Select id={`${id}-status`} className="w-36" value={status} onChange={(e) => setStatus(e.target.value)}>
            {STATUS_FILTERS.map((s) => (
              <option key={s.value} value={s.value}>
                {s.label}
              </option>
            ))}
          </Select>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-ip`}>Client IP</Label>
          <Input
            id={`${id}-ip`}
            className="w-40 font-mono"
            value={ip}
            onChange={(e) => setIp(e.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-q`}>Path contains</Label>
          <Input
            id={`${id}-q`}
            className="w-48 font-mono"
            value={text}
            onChange={(e) => setText(e.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </div>
        <Button type="button" variant="outline" aria-pressed={paused} onClick={() => setPaused((p) => !p)}>
          {paused ? <Play aria-hidden="true" /> : <Pause aria-hidden="true" />}
          {paused ? "Resume" : "Pause"}
        </Button>
      </div>

      {query.error ? (
        <ErrorState
          error={query.error}
          fallback="The request feed is not available."
          onRetry={() => void query.refetch()}
        />
      ) : rows.length === 0 ? (
        <p className="text-muted-foreground border-border rounded-md border border-dashed p-6 text-sm" role="status">
          {query.isPending ? "Loading requests…" : "No requests match yet. New ones appear here as they arrive."}
        </p>
      ) : (
        <div className="max-h-[32rem] overflow-auto rounded-lg border">
          <table className="w-full text-sm">
            <caption className="sr-only">Most recent requests, newest first</caption>
            <thead className="bg-muted text-muted-foreground sticky top-0 text-left text-xs">
              <tr>
                <th scope="col" className="px-3 py-1.5 font-medium">Time</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Status</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Method</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Path</th>
                {site === "" && <th scope="col" className="px-3 py-1.5 font-medium">Site</th>}
                <th scope="col" className="px-3 py-1.5 font-medium">Client</th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">Time taken</th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">Size</th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {pager.rows.map((r) => (
                <FeedRow key={r.seq} r={r} showSite={site === ""} />
              ))}
            </tbody>
          </table>
        </div>
      )}
      <TablePagination pager={pager} />
      <p className="text-muted-foreground text-xs" aria-live="polite">
        {paused ? "Paused. Resume to catch up." : `Showing the newest ${rows.length} requests.`}
      </p>
    </section>
  )
}
