import { describe, expect, it } from "vitest"

import {
  customProblem,
  isDateString,
  localDateString,
  MAX_CUSTOM_DAYS,
  spanKey,
  spanName,
  spanQuery,
} from "@/lib/metrics"

/** Local midnight, as the browser computes it: the tests pin the zone-dependent maths without assuming one. */
const local = (y: number, m: number, d: number) => Math.floor(new Date(y, m - 1, d).getTime() / 1000)

describe("dates", () => {
  it("accepts a real calendar date only", () => {
    expect(isDateString("2026-11-01")).toBe(true)
    expect(isDateString("2026-02-29")).toBe(false)
    expect(isDateString("2026-13-01")).toBe(false)
    expect(isDateString("2026-1-01")).toBe(false)
    expect(isDateString(20261101)).toBe(false)
  })

  it("formats the local date with a zero-padded month and day", () => {
    expect(localDateString(new Date(2026, 0, 5, 23, 30))).toBe("2026-01-05")
  })
})

describe("spanQuery", () => {
  it("asks by preset name for a preset", () => {
    expect(spanQuery("6h")).toBe("range=6h")
  })

  it("sends a custom window as unix seconds from the start of the first day to the end of the last", () => {
    const now = new Date(2026, 10, 25, 12).getTime()
    expect(spanQuery({ from: "2026-11-01", to: "2026-11-10" }, now)).toBe(
      `from=${local(2026, 11, 1)}&to=${local(2026, 11, 11)}`,
    )
  })

  it("caps a window that ends today at now", () => {
    const now = new Date(2026, 10, 25, 12, 0, 30).getTime()
    expect(spanQuery({ from: "2026-11-20", to: "2026-11-25" }, now)).toBe(
      `from=${local(2026, 11, 20)}&to=${Math.floor(now / 1000)}`,
    )
  })
})

describe("customProblem", () => {
  const now = new Date(2026, 10, 25, 12)

  it("passes a window that is ordered, not in the future, and within the daemon's limit", () => {
    expect(customProblem({ from: "2026-11-01", to: "2026-11-25" }, now)).toBeNull()
    expect(customProblem({ from: "2026-11-25", to: "2026-11-25" }, now)).toBeNull()
  })

  it("names a missing or impossible date", () => {
    expect(customProblem({ from: "", to: "2026-11-01" }, now)).toBe("Pick a start date and an end date.")
    expect(customProblem({ from: "2026-02-31", to: "2026-03-01" }, now)).toBe("Pick a start date and an end date.")
  })

  it("names a start after the end", () => {
    expect(customProblem({ from: "2026-11-10", to: "2026-11-01" }, now)).toBe("The start date is after the end date.")
  })

  it("names a start in the future", () => {
    expect(customProblem({ from: "2026-11-26", to: "2026-11-30" }, now)).toBe("The start date is in the future.")
  })

  it("names a window wider than the daemon keeps answering", () => {
    expect(customProblem({ from: "2025-01-01", to: "2026-11-01" }, now)).toBe(
      `Pick a window of ${MAX_CUSTOM_DAYS} days or fewer.`,
    )
  })
})

describe("span helpers", () => {
  it("keys a window by its dates and a preset by its name", () => {
    expect(spanKey("1h")).toBe("1h")
    expect(spanKey({ from: "2026-11-01", to: "2026-11-10" })).toBe("2026-11-01..2026-11-10")
  })

  it("names a window for the card's note", () => {
    expect(spanName("24h")).toBe("24 hours")
    expect(spanName({ from: "2026-11-01", to: "2026-11-10" })).toBe("2026-11-01 to 2026-11-10")
    expect(spanName({ from: "2026-11-01", to: "2026-11-01" })).toBe("2026-11-01")
  })
})
