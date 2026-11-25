import { useId, useMemo, useState, type KeyboardEvent, type PointerEvent } from "react"

import { TablePagination, usePagedRows } from "@/components/ui/table-controls"
import {
  isNum,
  linePath,
  nearestIndex,
  niceCeil,
  peak,
  pointTime,
  stepLabel,
  tickTime,
  type Point,
} from "@/lib/timeseries"

/** One line (or one stack layer) of a chart. */
export interface ChartSeries {
  key: string
  label: string
  values: Point[]
  /** A CSS colour. Defaults to the next one of the palette. */
  color?: string
  /** An SVG dash pattern, so two series are told apart without colour. */
  dash?: string
  /** Text for the legend's tooltip, e.g. what a percentile means. */
  hint?: string
}

/** Existing tokens only. Past six series the palette repeats with a dash. */
const PALETTE = [
  "var(--primary)",
  "var(--info)",
  "var(--success)",
  "var(--warning)",
  "var(--foreground)",
  "var(--muted-foreground)",
]
const DASHES = [undefined, "6 3", "2 3"]

function styled(series: ChartSeries[]): (ChartSeries & { color: string; dash?: string })[] {
  return series.map((s, i) => ({
    ...s,
    color: s.color ?? PALETTE[i % PALETTE.length],
    dash: s.dash ?? (s.color ? undefined : DASHES[Math.floor(i / PALETTE.length) % DASHES.length]),
  }))
}

const W = 480
const H = 150
const LEFT = 56
const BOTTOM = 18
const TOP = 6
const PLOT_W = W - LEFT
const PLOT_H = H - BOTTOM - TOP

/** The rows of the "Show data" table, newest first, one page at a time. */
function DataTable({
  title,
  t,
  step,
  series,
  format,
  total,
}: {
  title: string
  t: number[]
  step: number
  series: ChartSeries[]
  format: (n: number) => string
  total: boolean
}) {
  const rows = useMemo(() => t.map((_, i) => i).reverse(), [t])
  const pager = usePagedRows(rows)
  const cell = (v: Point | undefined) => (isNum(v) ? format(v) : "–")
  return (
    <div className="mt-2">
      <div className="max-h-64 overflow-auto rounded-md border">
        <table className="w-full text-xs">
          <caption className="sr-only">{`${title}, one row per ${stepLabel(step)}, newest first. A dash means no data.`}</caption>
          <thead className="bg-muted text-muted-foreground sticky top-0 text-left">
            <tr>
              <th scope="col" className="px-3 py-1 font-medium">
                Time
              </th>
              {series.map((s) => (
                <th key={s.key} scope="col" className="px-3 py-1 text-right font-medium">
                  {s.label}
                </th>
              ))}
              {total && (
                <th scope="col" className="px-3 py-1 text-right font-medium">
                  Total
                </th>
              )}
            </tr>
          </thead>
          <tbody className="divide-border divide-y">
            {pager.rows.map((i) => {
              const present = series.map((s) => s.values[i]).filter(isNum)
              return (
                <tr key={t[i]}>
                  <td className="px-3 py-1 tabular-nums">{pointTime(t[i], step)}</td>
                  {series.map((s) => (
                    <td key={s.key} className="px-3 py-1 text-right tabular-nums">
                      {cell(s.values[i])}
                    </td>
                  ))}
                  {total && (
                    <td className="px-3 py-1 text-right tabular-nums">
                      {present.length === 0 ? "–" : format(present.reduce((a, b) => a + b, 0))}
                    </td>
                  )}
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      <TablePagination pager={pager} />
    </div>
  )
}

/**
 * A time series as inline SVG, no chart library: several series, a legend, a
 * crosshair with a readout under the pointer, and the same readout for the
 * keyboard (arrow keys move the point, Home and End jump, Escape lets go).
 *
 * A `null` is a hole in the history, not a zero: a line breaks there and a
 * stacked bar is simply absent. Every value is also in the table under "Show
 * data", and each series has a name and a dash pattern in the legend, so colour
 * is never the only cue.
 */
export function TimeSeriesChart({
  title,
  t,
  step,
  series: rawSeries,
  format,
  ceiling,
  stacked = false,
  footnote,
}: {
  /** The chart's accessible name and its visible heading. */
  title: string
  /** Unix seconds of each point's start. */
  t: number[]
  /** Seconds per point. */
  step: number
  series: ChartSeries[]
  format: (n: number) => string
  /** A fixed top of the scale (100 for a percentage); the data's own nice ceiling otherwise. */
  ceiling?: number
  /** Stack the series as bars, one bar per point, for counts that add up. */
  stacked?: boolean
  footnote?: string
}) {
  const id = useId()
  const series = useMemo(() => styled(rawSeries), [rawSeries])
  const [cursor, setCursor] = useState<number | null>(null)
  const [announce, setAnnounce] = useState("")

  const n = t.length
  const t0 = n > 0 ? t[0] : 0
  const span = n > 1 ? t[n - 1] - t0 : 0
  const x = (i: number) => (n <= 1 ? LEFT + PLOT_W / 2 : LEFT + ((t[i] - t0) / span) * PLOT_W)

  const totals = useMemo(
    () =>
      t.map((_, i) => {
        const present = series.map((s) => s.values[i]).filter(isNum)
        return present.length === 0 ? null : present.reduce((a, b) => a + b, 0)
      }),
    [t, series],
  )
  const top = ceiling ?? niceCeil(stacked ? peak([totals]) : peak(series.map((s) => s.values)))
  const y = (v: number) => TOP + PLOT_H - (Math.min(Math.max(v, 0), top) / top) * PLOT_H

  const hasData = series.some((s) => s.values.some(isNum))

  const readout = (i: number): string => {
    const parts = series.map((s) => `${s.label} ${isNum(s.values[i]) ? format(s.values[i] as number) : "no data"}`)
    if (stacked && isNum(totals[i])) parts.push(`Total ${format(totals[i] as number)}`)
    return `${pointTime(t[i], step)}: ${parts.join(", ")}`
  }

  const label = !hasData
    ? `${title}: no data`
    : `${title}: ${series.length} series, ${n} points from ${pointTime(t[0], step)} to ${pointTime(t[n - 1], step)}. Focus and use the left and right arrow keys to read the values.`

  const move = (i: number, speak: boolean) => {
    const next = Math.min(Math.max(i, 0), n - 1)
    setCursor(next)
    if (speak) setAnnounce(readout(next))
  }

  const onKeyDown = (e: KeyboardEvent<SVGSVGElement>) => {
    if (n === 0) return
    const at = cursor ?? n - 1
    const jump = e.shiftKey ? 10 : 1
    let next: number | null = null
    if (e.key === "ArrowLeft") next = at - jump
    else if (e.key === "ArrowRight") next = at + jump
    else if (e.key === "Home") next = 0
    else if (e.key === "End") next = n - 1
    else if (e.key === "Escape") {
      setCursor(null)
      setAnnounce("")
      return
    } else return
    e.preventDefault()
    move(next, true)
  }

  const onPointerMove = (e: PointerEvent<SVGSVGElement>) => {
    const rect = e.currentTarget.getBoundingClientRect()
    if (rect.width === 0 || n === 0) return
    const vx = ((e.clientX - rect.left) / rect.width) * W
    if (vx < LEFT) return
    const at = t0 + ((vx - LEFT) / PLOT_W) * span
    const i = nearestIndex(t, at)
    if (i >= 0) setCursor(i)
  }

  const barW = n === 0 ? 0 : Math.max(PLOT_W / n, 0.5)
  const shown = cursor !== null && cursor < n ? cursor : null

  return (
    <figure aria-labelledby={`${id}-title`} className="flex min-w-0 flex-col gap-1.5">
      <figcaption id={`${id}-title`} className="text-sm font-medium">
        {title}
      </figcaption>
      <svg
        role="img"
        aria-label={label}
        tabIndex={hasData ? 0 : undefined}
        viewBox={`0 0 ${W} ${H}`}
        className="focus-visible:ring-ring/50 h-auto w-full rounded-md outline-none focus-visible:ring-2"
        onKeyDown={onKeyDown}
        onPointerMove={onPointerMove}
        onPointerLeave={() => setCursor(null)}
        onBlur={() => setCursor(null)}
      >
        {[0, 0.5, 1].map((f) => (
          <g key={f}>
            <line
              x1={LEFT}
              x2={W}
              y1={y(top * f)}
              y2={y(top * f)}
              stroke="var(--border)"
              strokeDasharray={f === 0 ? undefined : "3 3"}
            />
            <text x={LEFT - 4} y={y(top * f) + 3} textAnchor="end" fill="var(--muted-foreground)" fontSize="11">
              {format(top * f)}
            </text>
          </g>
        ))}
        {n > 0 && (
          <>
            <text x={LEFT} y={H - 3} fill="var(--muted-foreground)" fontSize="11">
              {tickTime(t[0], span)}
            </text>
            <text x={W} y={H - 3} textAnchor="end" fill="var(--muted-foreground)" fontSize="11">
              {tickTime(t[n - 1], span)}
            </text>
          </>
        )}

        {stacked
          ? t.map((_, i) => {
              let base = 0
              return (
                <g key={t[i]}>
                  {series.map((s) => {
                    const v = s.values[i]
                    if (!isNum(v) || v <= 0) return null
                    const yTop = y(base + v)
                    const yBottom = y(base)
                    base += v
                    return (
                      <rect
                        key={s.key}
                        data-series={s.key}
                        x={x(i) - barW / 2 + (barW > 2 ? 0.5 : 0)}
                        y={yTop}
                        width={Math.max(barW - (barW > 2 ? 1 : 0), 0.5)}
                        height={Math.max(yBottom - yTop, 0)}
                        fill={s.color}
                      />
                    )
                  })}
                </g>
              )
            })
          : series.map((s) => (
              <path
                key={s.key}
                data-series={s.key}
                d={linePath(s.values, x, y)}
                fill="none"
                stroke={s.color}
                strokeWidth="1.5"
                strokeDasharray={s.dash}
                strokeLinejoin="round"
                strokeLinecap="round"
                vectorEffect="non-scaling-stroke"
              />
            ))}

        {shown !== null && (
          <g aria-hidden="true" data-testid="crosshair">
            <line x1={x(shown)} x2={x(shown)} y1={TOP} y2={TOP + PLOT_H} stroke="var(--muted-foreground)" strokeWidth="1" />
            {!stacked &&
              series.map((s) => {
                const v = s.values[shown]
                return isNum(v) ? (
                  <circle key={s.key} cx={x(shown)} cy={y(v)} r="3" fill="var(--card)" stroke={s.color} strokeWidth="1.5" />
                ) : null
              })}
          </g>
        )}
      </svg>

      <p className="text-muted-foreground min-h-5 text-xs" data-testid="readout">
        {shown !== null ? readout(shown) : hasData ? "Hover or focus the chart to read a value." : "No data in this range."}
      </p>
      <div role="status" aria-live="polite" className="sr-only">
        {announce}
      </div>

      <ul className="text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs" aria-label={`${title} legend`}>
        {series.map((s) => (
          <li key={s.key} className="flex items-center gap-1.5" title={s.hint}>
            <svg aria-hidden="true" width="18" height="8" className="shrink-0">
              {stacked ? (
                <rect width="10" height="8" x="4" rx="1.5" fill={s.color} />
              ) : (
                <line x1="0" x2="18" y1="4" y2="4" stroke={s.color} strokeWidth="2" strokeDasharray={s.dash} />
              )}
            </svg>
            {s.label}
          </li>
        ))}
      </ul>
      {footnote && <p className="text-muted-foreground text-xs">{footnote}</p>}

      <details className="text-sm">
        <summary className="text-muted-foreground cursor-pointer text-xs">Show data</summary>
        <DataTable title={title} t={t} step={step} series={series} format={format} total={stacked} />
      </details>
    </figure>
  )
}
