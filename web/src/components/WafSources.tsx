import { lazy, Suspense } from "react"

import { ErrorState } from "@/components/ErrorState"
import { Button } from "@/components/ui/button"
import { SkeletonRows } from "@/components/ui/skeleton"
import { compactCount, countryName, type TrafficRange } from "@/lib/waf"
import { useDownloadWafGeo, useWafSources } from "@/lib/wafQueries"

/** The map and its atlas load when the page first shows sources, not with the app. */
const WorldMap = lazy(() => import("@/components/WorldMap").then((m) => ({ default: m.WorldMap })))

const PERIOD: Record<TrafficRange, string> = { "5m": "5 minutes", "15m": "15 minutes", "1h": "hour" }

/**
 * Where the requests of the period came from: a world map shaded by country, the
 * countries and the busiest addresses as tables, and the database's credit.
 * The map needs the DB-IP country database; without it the page says how to get
 * one and the tables still show the addresses.
 */
export function SourcesPanel({ range }: { range: TrafficRange }) {
  const sources = useWafSources(range)
  const download = useDownloadWafGeo()

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
  const hasRequests = data.requests > 0

  return (
    <section aria-labelledby="waf-sources-heading" className="flex flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <h2 id="waf-sources-heading" className="text-sm font-semibold">
            Where requests come from
          </h2>
          <p className="text-muted-foreground text-xs">
            {compactCount(data.requests)} requests in the last {PERIOD[range]}
            {data.blocked > 0 && `, ${compactCount(data.blocked)} blocked by the firewall`}.
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={download.isPending}
          onClick={() => download.mutate()}
        >
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

      <div className="bg-card rounded-md border p-3">
        <Suspense fallback={<div className="bg-muted h-48 w-full animate-pulse rounded-md" aria-hidden="true" />}>
          <WorldMap countries={data.countries} label="World map of where requests came from" />
        </Suspense>
        {!hasRequests && <p className="text-muted-foreground pt-2 text-center text-xs">No requests in this period yet.</p>}
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <div className="flex flex-col gap-2">
          <h3 className="text-sm font-medium">Countries</h3>
          {data.countries.length === 0 ? (
            <p className="text-muted-foreground text-sm">No country is known for this period.</p>
          ) : (
            <table className="w-full text-sm" aria-label="Requests by country">
              <caption className="sr-only">Requests by country, busiest first</caption>
              <thead>
                <tr className="text-muted-foreground border-b text-left text-xs">
                  <th scope="col" className="py-2 font-medium">Country</th>
                  <th scope="col" className="py-2 text-right font-medium">Requests</th>
                  <th scope="col" className="py-2 text-right font-medium">Blocked</th>
                  <th scope="col" className="py-2 text-right font-medium">Addresses</th>
                </tr>
              </thead>
              <tbody>
                {data.countries.map((c) => (
                  <tr key={c.code} className="border-b last:border-0">
                    <th scope="row" className="py-2 text-left font-normal">
                      {countryName(c.code)}
                    </th>
                    <td className="py-2 text-right tabular-nums">{c.requests.toLocaleString()}</td>
                    <td className="py-2 text-right tabular-nums">{c.blocked.toLocaleString()}</td>
                    <td className="py-2 text-right tabular-nums">{c.addresses.toLocaleString()}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        <div className="flex flex-col gap-2">
          <h3 className="text-sm font-medium">Busiest addresses</h3>
          {data.top_ips.length === 0 ? (
            <p className="text-muted-foreground text-sm">No requests in this period.</p>
          ) : (
            <table className="w-full text-sm" aria-label="Busiest client addresses">
              <caption className="sr-only">Client addresses, busiest first</caption>
              <thead>
                <tr className="text-muted-foreground border-b text-left text-xs">
                  <th scope="col" className="py-2 font-medium">Address</th>
                  <th scope="col" className="py-2 font-medium">Country</th>
                  <th scope="col" className="py-2 text-right font-medium">Requests</th>
                  <th scope="col" className="py-2 text-right font-medium">Blocked</th>
                </tr>
              </thead>
              <tbody>
                {data.top_ips.map((r) => (
                  <tr key={r.ip} className="border-b last:border-0">
                    <th scope="row" className="py-2 text-left font-normal break-all">
                      {r.ip}
                    </th>
                    <td className="py-2">{r.country ? countryName(r.country) : "Unknown"}</td>
                    <td className="py-2 text-right tabular-nums">{r.requests.toLocaleString()}</td>
                    <td className="py-2 text-right tabular-nums">{r.blocked.toLocaleString()}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {(data.unlocated > 0 || data.untracked > 0) && (
        <p className="text-muted-foreground text-xs">
          {data.unlocated > 0 && `${data.unlocated.toLocaleString()} requests came from addresses no country covers, such as private networks. `}
          {data.untracked > 0 &&
            `${data.untracked.toLocaleString()} requests are not attributed: the monitor keeps only the busiest addresses in each interval.`}
        </p>
      )}

      <p className="text-muted-foreground text-xs">
        {geo.installed
          ? `Country data from the DB-IP country database, ${geo.month}, ${geo.entries.toLocaleString()} ranges. `
          : ""}
        <a className="underline underline-offset-4" href={geo.attribution_url} target="_blank" rel="noreferrer">
          {geo.attribution}
        </a>
        {" · "}
        <a className="underline underline-offset-4" href={geo.license_url} target="_blank" rel="noreferrer">
          CC BY 4.0
        </a>
      </p>
    </section>
  )
}
