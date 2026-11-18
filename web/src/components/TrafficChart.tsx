import { compactCount, type TrafficPoint } from "@/lib/waf"

/** One colour per status class. The legend names each, so colour is never the only cue. */
export const CLASS_SERIES = [
  { key: "s2xx", label: "2xx success", color: "var(--success)" },
  { key: "s3xx", label: "3xx redirect", color: "var(--info)" },
  { key: "s4xx", label: "4xx client error", color: "var(--warning)" },
  { key: "s5xx", label: "5xx server error", color: "var(--destructive)" },
] as const

function clock(unixSeconds: number): string {
  return new Date(unixSeconds * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })
}

/**
 * Requests per time bucket, stacked by status class, as inline SVG. The numbers
 * are also in the table under "Show the data", so the picture is never the only
 * place a value appears.
 */
export function TrafficChart({ series, stepSeconds }: { series: TrafficPoint[]; stepSeconds: number }) {
  const width = 640
  const height = 160
  const left = 36
  const bottom = 18
  const plotW = width - left
  const plotH = height - bottom

  const total = series.reduce((n, p) => n + p.requests, 0)
  const peak = Math.max(1, ...series.map((p) => p.requests))
  const barW = series.length === 0 ? 0 : plotW / series.length
  const label =
    series.length === 0
      ? "Requests by status class: no traffic yet"
      : `Requests by status class from ${clock(series[0].t)} to ${clock(series[series.length - 1].t)}: ${total} requests, busiest ${stepSeconds}-second interval ${peak}`

  return (
    <figure className="flex flex-col gap-2">
      <svg role="img" aria-label={label} viewBox={`0 0 ${width} ${height}`} className="h-44 w-full">
        {/* Gridline and scale: top of the scale and zero. */}
        <line x1={left} x2={width} y1={0.5} y2={0.5} stroke="var(--border)" strokeDasharray="3 3" />
        <line x1={left} x2={width} y1={plotH} y2={plotH} stroke="var(--border)" />
        <text x={left - 4} y={10} textAnchor="end" fill="var(--muted-foreground)" fontSize="10">
          {compactCount(peak)}
        </text>
        <text x={left - 4} y={plotH} textAnchor="end" fill="var(--muted-foreground)" fontSize="10">
          0
        </text>
        {series.length > 0 && (
          <>
            <text x={left} y={height - 4} fill="var(--muted-foreground)" fontSize="10">
              {clock(series[0].t)}
            </text>
            <text x={width} y={height - 4} textAnchor="end" fill="var(--muted-foreground)" fontSize="10">
              {clock(series[series.length - 1].t)}
            </text>
          </>
        )}
        {series.map((p, i) => {
          let y = plotH
          return (
            <g key={p.t}>
              <title>{`${clock(p.t)}: ${p.requests} requests, ${p.s4xx} 4xx, ${p.s5xx} 5xx`}</title>
              {CLASS_SERIES.map((c) => {
                const h = (p[c.key] / peak) * plotH
                y -= h
                return h > 0 ? (
                  <rect
                    key={c.key}
                    x={left + i * barW + 0.5}
                    y={y}
                    width={Math.max(barW - 1, 1)}
                    height={h}
                    fill={c.color}
                  />
                ) : null
              })}
            </g>
          )
        })}
      </svg>
      <ul className="text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs" aria-label="Chart legend">
        {CLASS_SERIES.map((c) => (
          <li key={c.key} className="flex items-center gap-1.5">
            <span aria-hidden="true" className="inline-block size-2.5 rounded-sm" style={{ background: c.color }} />
            {c.label}
          </li>
        ))}
      </ul>
      <details className="text-sm">
        <summary className="text-muted-foreground cursor-pointer text-xs">Show the data</summary>
        <div className="mt-2 max-h-64 overflow-auto rounded-md border">
          <table className="w-full text-xs">
            <caption className="sr-only">Requests per {stepSeconds} seconds by status class</caption>
            <thead className="bg-muted text-muted-foreground sticky top-0 text-left">
              <tr>
                <th scope="col" className="px-3 py-1 font-medium">Time</th>
                <th scope="col" className="px-3 py-1 text-right font-medium">Requests</th>
                <th scope="col" className="px-3 py-1 text-right font-medium">2xx</th>
                <th scope="col" className="px-3 py-1 text-right font-medium">3xx</th>
                <th scope="col" className="px-3 py-1 text-right font-medium">4xx</th>
                <th scope="col" className="px-3 py-1 text-right font-medium">5xx</th>
                <th scope="col" className="px-3 py-1 text-right font-medium">WAF events</th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {[...series].reverse().map((p) => (
                <tr key={p.t}>
                  <td className="px-3 py-1 tabular-nums">{clock(p.t)}</td>
                  <td className="px-3 py-1 text-right tabular-nums">{p.requests}</td>
                  <td className="px-3 py-1 text-right tabular-nums">{p.s2xx}</td>
                  <td className="px-3 py-1 text-right tabular-nums">{p.s3xx}</td>
                  <td className="px-3 py-1 text-right tabular-nums">{p.s4xx}</td>
                  <td className="px-3 py-1 text-right tabular-nums">{p.s5xx}</td>
                  <td className="px-3 py-1 text-right tabular-nums">{p.waf_events}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </figure>
  )
}
