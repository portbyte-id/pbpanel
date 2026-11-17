import { sparkPoints } from "@/lib/sysinfo"

/**
 * A small line of recent samples. It is a picture of the number printed beside
 * it, never the only place a value appears, so it is exposed as one image with
 * a text label and carries no axes.
 */
export function Sparkline({
  values,
  label,
  ceiling,
  area = false,
  className,
}: {
  /** Fill under the line, for the larger detail chart. */
  area?: boolean
  values: number[]
  /** What a screen reader hears, e.g. "CPU over the last hour, now 12%". */
  label: string
  /** Fixed top of the scale (100 for a percentage); the series maximum when omitted. */
  ceiling?: number
  className?: string
}) {
  const width = 120
  const height = 32
  if (values.length < 2) {
    return <div role="img" aria-label={`${label}: no history yet`} className={className} />
  }
  return (
    <svg
      role="img"
      aria-label={label}
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      className={className}
    >
      {area && (
        <polygon
          points={`0,${height} ${sparkPoints(values, width, height, ceiling)} ${width},${height}`}
          fill="currentColor"
          fillOpacity="0.12"
        />
      )}
      <polyline
        points={sparkPoints(values, width, height, ceiling)}
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
