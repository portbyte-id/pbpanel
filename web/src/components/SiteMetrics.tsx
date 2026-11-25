import { useMemo } from "react"

import { EMPTY_HISTORY_TEXT, HistoryNotice } from "@/components/HistoryCard"
import { RangePicker } from "@/components/RangePicker"
import { CLASS_SERIES } from "@/components/TrafficChart"
import { TimeSeriesChart } from "@/components/TimeSeriesChart"
import { SkeletonRows } from "@/components/ui/skeleton"
import {
  classifyHistory,
  hasNoData,
  PERCENTILE_NOTE,
  percentileTooltip,
  useRangeParam,
  useSiteHistory,
} from "@/lib/metrics"
import { formatBytes } from "@/lib/sysinfo"
import { formatSeconds, stepLabel } from "@/lib/timeseries"
import { compactCount } from "@/lib/waf"

export const UPSTREAM_PENDING_TEXT =
  "Upstream time is not available yet: it appears once nginx has reloaded with the new log format."

/** The site's recorded history: requests by status class, bytes, and request and upstream time. */
export function SiteMetrics({ domain }: { domain: string }) {
  const [range, setRange] = useRangeParam()
  const q = useSiteHistory(domain, range)
  const state = classifyHistory(q)
  const h = q.data

  const requestSeries = useMemo(() => {
    if (!h) return []
    const cols = [h.s2, h.s3, h.s4, h.s5]
    return CLASS_SERIES.map((c, i) => ({ key: c.key, label: c.label, color: c.color, values: cols[i] }))
  }, [h])

  let body
  if (state.kind === "loading") {
    body = <SkeletonRows label="Loading metrics…" />
  } else if (state.kind !== "ready") {
    body = <HistoryNotice state={state} />
  } else if (!h || h.t.length === 0 || hasNoData(h.req, h.s2, h.s3, h.s4, h.s5, h.bytes)) {
    body = (
      <p role="status" className="text-muted-foreground text-sm">
        {EMPTY_HISTORY_TEXT} Requests appear once the site has served some.
      </p>
    )
  } else {
    const upstream = !hasNoData(h.urt_p50, h.urt_p95)
    body = (
      <div aria-busy={q.isPlaceholderData} className="flex flex-col gap-5 data-[stale=true]:opacity-60" data-stale={q.isPlaceholderData}>
        <TimeSeriesChart
          title="Requests by status class"
          t={h.t}
          step={h.step}
          series={requestSeries}
          format={(v) => compactCount(Math.round(v))}
          stacked
          footnote={`Requests per ${stepLabel(h.step)}.`}
        />
        <TimeSeriesChart
          title="Bytes sent"
          t={h.t}
          step={h.step}
          series={[{ key: "bytes", label: "Bytes sent", values: h.bytes }]}
          format={formatBytes}
          footnote={`Bytes per ${stepLabel(h.step)}.`}
        />
        <TimeSeriesChart
          title="Request time"
          t={h.t}
          step={h.step}
          format={formatSeconds}
          series={[
            { key: "rt_p50", label: "p50", values: h.rt_p50, hint: percentileTooltip("p50") },
            { key: "rt_p95", label: "p95", values: h.rt_p95, dash: "6 3", hint: percentileTooltip("p95") },
          ]}
          footnote={PERCENTILE_NOTE}
        />
        {upstream ? (
          <TimeSeriesChart
            title="Upstream time"
            t={h.t}
            step={h.step}
            format={formatSeconds}
            series={[
              { key: "urt_p50", label: "p50", values: h.urt_p50, hint: percentileTooltip("p50", "upstream responses") },
              {
                key: "urt_p95",
                label: "p95",
                values: h.urt_p95,
                dash: "6 3",
                hint: percentileTooltip("p95", "upstream responses"),
              },
            ]}
            footnote={`${PERCENTILE_NOTE} Time the backend (PHP-FPM or the proxied app) took to answer.`}
          />
        ) : (
          <p role="status" className="text-muted-foreground text-sm" data-testid="upstream-pending">
            {UPSTREAM_PENDING_TEXT}
          </p>
        )}
      </div>
    )
  }

  return (
    <section aria-labelledby="site-metrics-heading" className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 id="site-metrics-heading" className="text-sm font-semibold">
            Metrics
          </h3>
          <p className="text-muted-foreground text-xs">What nginx served for {domain}, from its access log.</p>
        </div>
        <RangePicker value={range} onChange={setRange} />
      </div>
      {body}
    </section>
  )
}
