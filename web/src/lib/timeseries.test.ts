import { describe, expect, it } from "vitest"

import { formatSeconds, linePath, nearestIndex, niceCeil, peak, runs, stepLabel } from "@/lib/timeseries"

describe("runs", () => {
  it("splits at every null", () => {
    expect(runs([1, 2, null, 4, null, null, 7, 8])).toEqual([
      { start: 0, end: 1 },
      { start: 3, end: 3 },
      { start: 6, end: 7 },
    ])
  })

  it("is empty when there is no data", () => {
    expect(runs([null, null])).toEqual([])
    expect(runs([])).toEqual([])
  })

  it("treats NaN as a hole", () => {
    expect(runs([1, Number.NaN, 3])).toHaveLength(2)
  })
})

describe("linePath", () => {
  const x = (i: number) => i * 10
  const y = (v: number) => v

  it("starts a new sub-path after a null instead of drawing across it", () => {
    const d = linePath([1, 2, null, 4, 5], x, y)
    expect(d.match(/M/g)).toHaveLength(2)
    expect(d).toBe("M0.0,1.0L10.0,2.0M30.0,4.0L40.0,5.0")
    // The point at index 2 (x = 20) is never an end of a segment.
    expect(d).not.toContain("20.0,")
  })

  it("draws a lone point as a zero-length segment", () => {
    expect(linePath([null, 3, null], x, y)).toBe("M10.0,3.0h0")
  })

  it("is empty without data", () => {
    expect(linePath([null], x, y)).toBe("")
  })
})

describe("scales", () => {
  it("rounds a maximum up to a readable top", () => {
    expect(niceCeil(0)).toBe(1)
    expect(niceCeil(0.7)).toBe(1)
    expect(niceCeil(13)).toBe(20)
    expect(niceCeil(230)).toBe(250)
    expect(niceCeil(4100)).toBe(5000)
  })

  it("finds the peak over several series, skipping nulls", () => {
    expect(peak([[1, null, 3], [2, 9, null]])).toBe(9)
    expect(peak([[null]])).toBe(0)
  })

  it("finds the nearest time", () => {
    const t = [100, 160, 220]
    expect(nearestIndex(t, 100)).toBe(0)
    expect(nearestIndex(t, 140)).toBe(1)
    expect(nearestIndex(t, 1000)).toBe(2)
    expect(nearestIndex([], 5)).toBe(-1)
  })
})

describe("wording", () => {
  it("formats durations", () => {
    expect(formatSeconds(0.82)).toBe("820 ms")
    expect(formatSeconds(1.4)).toBe("1.40 s")
    expect(formatSeconds(12.34)).toBe("12.3 s")
    expect(formatSeconds(125)).toBe("2 min 5 s")
    expect(formatSeconds(-1)).toBe("–")
  })

  it("names a step", () => {
    expect(stepLabel(30)).toBe("30 seconds")
    expect(stepLabel(60)).toBe("1 minute")
    expect(stepLabel(900)).toBe("15 minutes")
    expect(stepLabel(3600)).toBe("1 hour")
  })
})
