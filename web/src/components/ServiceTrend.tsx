import type { ServiceHistory } from "@/lib/metrics"
import { formatPercent } from "@/lib/sysinfo"
import { isNum, linePath, peak } from "@/lib/timeseries"

const W = 96
const H = 24

/**
 * One service's last hour in a grid cell: its CPU as a line that breaks where the
 * daemon recorded nothing, and a grey band over every stretch the service was
 * not running (the stronger the band, the more of the bucket it was down). The
 * words carry the same facts, so the picture is never the only place they appear.
 */
export function ServiceTrend({ history, t }: { history: ServiceHistory | undefined; t: number[] }) {
  if (!history || (history.cpu.every((v) => !isNum(v)) && history.up_frac.every((v) => !isNum(v)))) {
    return (
      <span className="text-muted-foreground text-xs" role="img" aria-label="No history yet">
        –
      </span>
    )
  }
  const n = t.length
  const x = (i: number) => (n <= 1 ? W / 2 : (i / (n - 1)) * W)
  // The scale tops out at 10% of a core at least, so an idle service is a flat line, not noise.
  const top = Math.max(peak([history.cpu]), 10)
  const y = (v: number) => H - 2 - (Math.min(Math.max(v, 0), top) / top) * (H - 4)
  const slot = n <= 1 ? W : W / (n - 1)

  const known = history.up_frac.filter(isNum)
  const upShare = known.length === 0 ? null : known.reduce((a, b) => a + b, 0) / known.length
  const cpuKnown = history.cpu.filter(isNum)
  const cpuPeak = cpuKnown.length === 0 ? null : Math.max(...cpuKnown)
  const label = [
    `${history.name} over the last hour`,
    cpuPeak === null ? "no CPU recorded" : `CPU peak ${formatPercent(cpuPeak)} of a core`,
    upShare === null ? null : `running ${formatPercent(upShare * 100)} of the time`,
  ]
    .filter(Boolean)
    .join(", ")

  return (
    <svg role="img" aria-label={label} viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="text-primary h-6 w-full">
      {history.up_frac.map((f, i) =>
        isNum(f) && f < 1 ? (
          <rect
            key={t[i] ?? i}
            data-testid="down-band"
            x={x(i) - slot / 2}
            y={0}
            width={slot}
            height={H}
            fill="currentColor"
            className="text-muted-foreground"
            fillOpacity={0.15 + 0.35 * (1 - f)}
          />
        ) : null,
      )}
      <path
        d={linePath(history.cpu, x, y)}
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinejoin="round"
        strokeLinecap="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  )
}
