import { lazy, Suspense, useState } from "react"

import { ErrorState } from "@/components/ErrorState"
import { Panel } from "@/components/TrafficPanels"
import { Button } from "@/components/ui/button"
import { SkeletonRows } from "@/components/ui/skeleton"
import { compactCount, countryName, type TrafficRange, type WafSources } from "@/lib/waf"
import { useDownloadWafGeo, useWafSources } from "@/lib/wafQueries"

/** The map and its atlas load when the page first shows sources, not with the app. */
const WorldMap = lazy(() => import("@/components/WorldMap").then((m) => ({ default: m.WorldMap })))

const PERIOD: Record<TrafficRange, string> = { "5m": "5 minutes", "15m": "15 minutes", "1h": "hour" }

type MapMetric = "requests" | "blocked"

/** The addresses the firewall blocked most, busiest first, ten at most. */
export function blockedRanking(rows: WafSources["top_ips"]) {
  return rows
    .filter((r) => r.blocked > 0)
    .sort((a, b) => b.blocked - a.blocked)
    .slice(0, 10)
}

/**
 * The access and block map: a compact world map shaded by requests or by blocks,
 * beside the ten addresses blocked most. The map needs the DB-IP country database;
 * without it the page says how to get one and the ranking still shows the addresses.
 */
export function SourcesPanel({ range }: { range: TrafficRange }) {
  const sources = useWafSources(range)
  const download = useDownloadWafGeo()
  const [metric, setMetric] = useState<MapMetric>("requests")

  if (sources.isPending) return <SkeletonRows label="Loading where requests come from…" />
  if (sources.error) {
    return (
      <ErrorState
        error={sources.error}
        fallback="The sources of the traffic are not available."
        onRetry={() => void sources.refetch()}
      />
    )
  }
  const data = sources.data
  const geo = data.geo
  const ranking = blockedRanking(data.top_ips)

  return (
    <Panel
      title="Access/Block Map"
      aside={
        <div role="group" aria-label="Map shows" className="bg-muted inline-flex rounded-md p-0.5">
          {(["requests", "blocked"] as const).map((m) => (
            <button
              key={m}
              type="button"
              aria-pressed={metric === m}
              onClick={() => setMetric(m)}
              className={
                "rounded-sm px-2.5 py-1 text-xs font-medium " +
                (metric === m ? "bg-card text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")
              }
            >
              {m === "requests" ? "Requests" : "Blocks"}
            </button>
          ))}
        </div>
      }
    >
      <div className="text-muted-foreground flex flex-wrap items-center justify-between gap-2 text-xs">
        <p>
          {compactCount(data.requests)} requests, {compactCount(data.blocked)} blocked in the last {PERIOD[range]}
        </p>
        <Button type="button" variant="outline" size="sm" disabled={download.isPending} onClick={() => download.mutate()}>
          {download.isPending ? "Downloading…" : geo.installed ? "Update country database" : "Download country database"}
        </Button>
      </div>

      {download.error && (
        <p role="alert" className="text-destructive text-sm">
          The country database could not be downloaded: {download.error.message}
        </p>
      )}

      {!geo.installed && (
        <p className="bg-muted text-muted-foreground rounded-md p-3 text-sm">
          The map places each address on a country using the free DB-IP country database. Download it once; lookups
          then happen on this host, and no address is sent to a third party.
        </p>
      )}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_16rem]">
        <div className="flex min-w-0 flex-col gap-2">
          <Suspense fallback={<div className="bg-muted h-52 w-full animate-pulse rounded-md md:h-64" aria-hidden="true" />}>
            <WorldMap
              countries={data.countries}
              metric={metric}
              label={`World map of where requests came from, shaded by ${metric === "requests" ? "requests" : "blocked requests"}`}
            />
          </Suspense>
          <div className="text-muted-foreground flex items-center justify-end gap-2 text-xs">
            <span>Low</span>
            <span
              aria-hidden="true"
              className="h-1.5 w-24 rounded-full"
              style={{ background: "linear-gradient(to right, var(--muted), var(--primary))" }}
            />
            <span>High</span>
          </div>
          {data.requests === 0 && (
            <p className="text-muted-foreground text-center text-xs">No requests in this period yet.</p>
          )}
        </div>

        <section aria-label="Blocked source ranking" className="flex min-w-0 flex-col gap-2">
          <h3 className="text-sm font-medium">
            Blocked source ranking <span className="text-muted-foreground text-xs font-normal">Top 10</span>
          </h3>
          {ranking.length === 0 ? (
            <p className="text-muted-foreground py-6 text-center text-sm">No Data</p>
          ) : (
            <table className="w-full text-xs" aria-label="Blocked client addresses">
              <caption className="sr-only">Client addresses the firewall blocked, most blocks first</caption>
              <thead>
                <tr className="text-muted-foreground border-b text-left">
                  <th scope="col" className="py-1.5 font-medium">Req IP</th>
                  <th scope="col" className="py-1.5 text-right font-medium">Blocks</th>
                  <th scope="col" className="py-1.5 pl-3 font-medium">IP attribution</th>
                </tr>
              </thead>
              <tbody>
                {ranking.map((r) => (
                  <tr key={r.ip} className="border-b last:border-0">
                    <th scope="row" className="py-1.5 text-left font-mono font-normal break-all">
                      {r.ip}
                    </th>
                    <td className="py-1.5 text-right tabular-nums">{r.blocked.toLocaleString("en-US")}</td>
                    <td className="py-1.5 pl-3">{r.country ? countryName(r.country) : "Unknown"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      </div>

      {(data.unlocated > 0 || data.untracked > 0) && (
        <p className="text-muted-foreground text-xs">
          {data.unlocated > 0 &&
            `${data.unlocated.toLocaleString("en-US")} requests came from addresses no country covers, such as private networks. `}
          {data.untracked > 0 &&
            `${data.untracked.toLocaleString("en-US")} requests are not attributed: the monitor keeps only the busiest addresses in each interval.`}
        </p>
      )}

      <p className="text-muted-foreground text-xs">
        {geo.installed
          ? `Country data from the DB-IP country database, ${geo.month}, ${geo.entries.toLocaleString("en-US")} ranges. `
          : ""}
        <a className="underline underline-offset-4" href={geo.attribution_url} target="_blank" rel="noreferrer">
          {geo.attribution}
        </a>
        {" · "}
        <a className="underline underline-offset-4" href={geo.license_url} target="_blank" rel="noreferrer">
          CC BY 4.0
        </a>
      </p>
    </Panel>
  )
}

/** Requests per country for the period, with the blocks and the distinct addresses behind them. */
export function CountriesTable({ range }: { range: TrafficRange }) {
  const sources = useWafSources(range)

  if (sources.isPending) return <SkeletonRows label="Loading countries…" />
  if (sources.error) {
    return (
      <ErrorState
        error={sources.error}
        fallback="The countries of the traffic are not available."
        onRetry={() => void sources.refetch()}
      />
    )
  }
  const rows = sources.data.countries

  return (
    <section aria-label="Countries" className="flex flex-col gap-2">
      <h2 className="text-sm font-semibold">Countries</h2>
      {rows.length === 0 ? (
        <p className="text-muted-foreground text-sm">No country is known for this period.</p>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-sm" aria-label="Requests by country">
            <caption className="sr-only">Requests by country, busiest first</caption>
            <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
              <tr>
                <th scope="col" className="px-3 py-1.5 font-medium">Country</th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">Requests</th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">Blocked</th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">Addresses</th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {rows.map((c) => (
                <tr key={c.code}>
                  <th scope="row" className="px-3 py-1.5 text-left font-normal">
                    {countryName(c.code)}
                  </th>
                  <td className="px-3 py-1.5 text-right tabular-nums">{c.requests.toLocaleString("en-US")}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{c.blocked.toLocaleString("en-US")}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{c.addresses.toLocaleString("en-US")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}
