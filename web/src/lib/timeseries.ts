/**
 * Geometry for the history charts. A point with no data is `null` and stays a
 * hole: the line breaks there and is never drawn across it.
 */

export type Point = number | null

export function isNum(v: unknown): v is number {
  return typeof v === "number" && Number.isFinite(v)
}

/** A "nice" top for a scale that has to hold `max`: 1, 2, 2.5, 5 or 10 times a power of ten. */
export function niceCeil(max: number): number {
  if (!isNum(max) || max <= 0) return 1
  const base = 10 ** Math.floor(Math.log10(max))
  const f = max / base
  const nice = f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10
  return nice * base
}

/** The largest finite value over all the series, or 0 when there is none. */
export function peak(series: Point[][]): number {
  let top = 0
  for (const s of series) for (const v of s) if (isNum(v) && v > top) top = v
  return top
}

/** Inclusive index ranges of consecutive points that have data. */
export function runs(values: Point[]): { start: number; end: number }[] {
  const out: { start: number; end: number }[] = []
  let start = -1
  values.forEach((v, i) => {
    if (isNum(v)) {
      if (start < 0) start = i
    } else if (start >= 0) {
      out.push({ start, end: i - 1 })
      start = -1
    }
  })
  if (start >= 0) out.push({ start, end: values.length - 1 })
  return out
}

const px = (n: number) => n.toFixed(1)

/**
 * An SVG path with one sub-path per run of data, so a null starts a new `M`
 * instead of a line across the hole. A run of one point is a zero-length
 * segment, which a round line cap draws as a dot.
 */
export function linePath(values: Point[], x: (i: number) => number, y: (v: number) => number): string {
  const parts: string[] = []
  for (const r of runs(values)) {
    let d = `M${px(x(r.start))},${px(y(values[r.start] as number))}`
    if (r.end === r.start) d += "h0"
    for (let i = r.start + 1; i <= r.end; i++) d += `L${px(x(i))},${px(y(values[i] as number))}`
    parts.push(d)
  }
  return parts.join("")
}

/** The index of the time nearest to `at`; -1 for an empty axis. */
export function nearestIndex(t: number[], at: number): number {
  if (t.length === 0 || !isNum(at)) return -1
  let lo = 0
  let hi = t.length - 1
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1
    if (t[mid] <= at) lo = mid
    else hi = mid
  }
  return Math.abs(t[lo] - at) <= Math.abs(t[hi] - at) ? lo : hi
}

/** "12:03" inside a day, "Nov 25, 12:03" across days. */
export function tickTime(unix: number, spanSeconds: number): string {
  const d = new Date(unix * 1000)
  const clock = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
  if (spanSeconds <= 24 * 3600) return clock
  return `${d.toLocaleDateString([], { month: "short", day: "numeric" })}, ${clock}`
}

/** The full stamp of one point, with seconds when the step is under a minute. */
export function pointTime(unix: number, stepSeconds: number): string {
  const d = new Date(unix * 1000)
  return d.toLocaleString([], {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    ...(stepSeconds < 60 ? { second: "2-digit" } : {}),
  })
}

/** "30 seconds", "1 minute", "15 minutes", "1 hour": the width of one chart point. */
export function stepLabel(step: number): string {
  if (step < 60) return `${step} ${step === 1 ? "second" : "seconds"}`
  if (step < 3600) {
    const m = Math.round(step / 60)
    return `${m} ${m === 1 ? "minute" : "minutes"}`
  }
  const h = Math.round(step / 3600)
  return `${h} ${h === 1 ? "hour" : "hours"}`
}

/** "820 ms", "1.4 s", "2 min 5 s": a duration in seconds, read at a glance. */
export function formatSeconds(seconds: number): string {
  if (!isNum(seconds) || seconds < 0) return "–"
  if (seconds < 1) return `${Math.round(seconds * 1000)} ms`
  if (seconds < 10) return `${seconds.toFixed(2)} s`
  if (seconds < 60) return `${seconds.toFixed(1)} s`
  const m = Math.floor(seconds / 60)
  return `${m} min ${Math.round(seconds - m * 60)} s`
}
