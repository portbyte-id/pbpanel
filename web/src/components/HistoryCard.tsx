import { Link } from "@tanstack/react-router"
import { useMemo, type ReactNode } from "react"

import { Panel } from "@/components/Panel"
import { TimeSeriesChart, type ChartSeries } from "@/components/TimeSeriesChart"
import { SkeletonRows } from "@/components/ui/skeleton"
import {
  classifyHistory,
  customProblem,
  hasNoData,
  spanName,
  useDisksHistory,
  useHostHistory,
  type Col,
  type HistorySpan,
  type HistoryState,
} from "@/lib/metrics"
import { formatBytes, formatPercent, formatRate } from "@/lib/sysinfo"
import { isNum } from "@/lib/timeseries"

export const EMPTY_HISTORY_TEXT = "History starts recording after the first minute."

/**
 * Why a history view has nothing to draw, in quiet words: recording switched off
 * (409) and a history database that did not open (503) are states, not alarms.
 */
export function HistoryNotice({ state }: { state: HistoryState }): ReactNode {
  if (state.kind === "disabled") {
    return (
      <p role="status" className="text-muted-foreground px-4 py-4 text-sm">
        History recording is turned off, so there is nothing to show. Switch on <code translate="no">metrics.enabled</code>{" "}
        under{" "}
        <Link to="/settings" search={{ tab: "Metrics" }} className="text-primary underline-offset-2 hover:underline">
          Settings, Metrics
        </Link>{" "}
        to record and view it.
      </p>
    )
  }
  if (state.kind === "unavailable") {
    return (
      <p role="status" className="text-muted-foreground px-4 py-4 text-sm">
        History is not available right now: the history database could not be opened. The live tiles are not affected.
        The daemon log says why.
      </p>
    )
  }
  if (state.kind === "error") {
    return (
      <p role="alert" className="text-destructive px-4 py-4 text-sm">
        {state.detail}
      </p>
    )
  }
  return null
}

/** The sum of several columns, point by point; a point is null only when every column is. */
export function sumColumns(cols: Col[]): Col {
  const n = cols.reduce((m, c) => Math.max(m, c.length), 0)
  return Array.from({ length: n }, (_, i) => {
    const present = cols.map((c) => c[i]).filter(isNum)
    return present.length === 0 ? null : present.reduce((a, b) => a + b, 0)
  })
}

/**
 * The History card: the host's CPU, load, memory, disks and network over the
 * span the page picked. A point the daemon did not record is a gap. A custom
 * window that cannot be asked for is explained here and not sent to the daemon.
 */
export function HistoryCard({ span }: { span: HistorySpan }) {
  const problem = typeof span === "string" ? null : customProblem(span)
  const host = useHostHistory(span, problem === null)
  const disks = useDisksHistory(span, problem === null)
  const state = classifyHistory(host)
  const h = host.data
  const d = disks.data

  const charts = useMemo(() => {
    if (!h) return null
    const hostCharts: { id: string; node: ReactNode }[] = []
    const cpu: ChartSeries[] = [
      { key: "cpu", label: "CPU average", values: h.cpu },
      { key: "cpu_max", label: "CPU peak", values: h.cpu_max, dash: "6 3" },
    ]
    hostCharts.push({
      id: "cpu",
      node: <TimeSeriesChart title="CPU" t={h.t} step={h.step} series={cpu} format={formatPercent} ceiling={100} />,
    })
    hostCharts.push({
      id: "load",
      node: (
        <TimeSeriesChart
          title="Load average"
          t={h.t}
          step={h.step}
          format={(v) => v.toFixed(2)}
          series={[
            { key: "load1", label: "1 minute", values: h.load1 },
            { key: "load5", label: "5 minutes", values: h.load5 },
            { key: "load15", label: "15 minutes", values: h.load15 },
          ]}
        />
      ),
    })
    hostCharts.push({
      id: "mem",
      node: (
        <TimeSeriesChart
          title="Memory and swap"
          t={h.t}
          step={h.step}
          format={formatBytes}
          series={[
            { key: "mem_used", label: "Memory used", values: h.mem_used },
            { key: "mem_avail", label: "Memory available", values: h.mem_avail },
            { key: "swap_used", label: "Swap used", values: h.swap_used },
          ]}
        />
      ),
    })
    hostCharts.push({
      id: "net",
      node: (
        <TimeSeriesChart
          title="Network"
          t={h.t}
          step={h.step}
          format={formatRate}
          series={[
            { key: "net_rx", label: "Received", values: h.net_rx },
            { key: "net_tx", label: "Sent", values: h.net_tx },
          ]}
        />
      ),
    })
    return hostCharts
  }, [h])

  const diskCharts = useMemo(() => {
    if (!d || d.mounts.length === 0) return null
    return [
      {
        id: "disk-use",
        node: (
          <TimeSeriesChart
            title="Disk used"
            t={d.t}
            step={d.step}
            format={formatPercent}
            ceiling={100}
            series={d.mounts.map((m) => ({ key: m.mount, label: m.mount, values: m.used_pct }))}
          />
        ),
      },
      {
        id: "disk-io",
        node: (
          <TimeSeriesChart
            title="Disk IO, all mounts"
            t={d.t}
            step={d.step}
            format={formatRate}
            series={[
              { key: "rd", label: "Read", values: sumColumns(d.mounts.map((m) => m.rd_bps)) },
              { key: "wr", label: "Write", values: sumColumns(d.mounts.map((m) => m.wr_bps)) },
            ]}
          />
        ),
      },
    ]
  }, [d])

  const empty = h !== undefined && (h.t.length === 0 || hasNoData(h.cpu, h.load1, h.mem_used, h.net_rx))
  const stale = host.isPlaceholderData

  let body: ReactNode
  if (problem) body = <p role="alert" className="text-destructive px-4 py-4 text-sm">{problem}</p>
  else if (state.kind === "loading") body = <div className="px-4 py-4"><SkeletonRows label="Loading history…" /></div>
  else if (state.kind !== "ready") body = <HistoryNotice state={state} />
  else if (empty) {
    body = (
      <p role="status" className="text-muted-foreground px-4 py-4 text-sm">
        {EMPTY_HISTORY_TEXT}
      </p>
    )
  } else {
    // CPU and Load, Memory and Network, then the disks: pairs that read together.
    const all = [...(charts ?? []), ...(diskCharts ?? [])]
    const order = ["cpu", "load", "mem", "net", "disk-use", "disk-io"]
    all.sort((a, b) => order.indexOf(a.id) - order.indexOf(b.id))
    body = (
      <div aria-busy={stale} className="grid gap-x-6 gap-y-5 p-4 data-[stale=true]:opacity-60 md:grid-cols-2" data-stale={stale}>
        {all.map((c) => (
          <div key={c.id} className="min-w-0">
            {c.node}
          </div>
        ))}
        {disks.error && !diskCharts && (
          <p className="text-muted-foreground text-xs md:col-span-2">The disk history could not be loaded.</p>
        )}
      </div>
    )
  }

  return (
    <Panel title="History" note={spanName(span)}>
      {body}
    </Panel>
  )
}
