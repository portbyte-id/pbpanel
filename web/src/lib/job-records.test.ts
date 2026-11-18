import { describe, expect, it } from "vitest"

import { formatDuration, isActiveJob, jobElapsedMs, type JobRecord } from "@/lib/jobs"

const base: JobRecord = {
  id: "3f9a1c2b8d7e4f60a1b2c3d4e5f60718",
  kind: "php.build",
  state: "running",
  created_at: "2026-11-18T09:00:00.000Z",
  started_at: "2026-11-18T09:00:05.000Z",
  exit_code: 0,
  error: "",
  last_seq: 0,
  dropped: 0,
  truncated: false,
}

describe("isActiveJob", () => {
  it("treats queued and running as live and every other state as finished", () => {
    expect(isActiveJob({ state: "queued" })).toBe(true)
    expect(isActiveJob({ state: "running" })).toBe(true)
    for (const state of ["succeeded", "failed", "canceled", "interrupted"] as const) {
      expect(isActiveJob({ state })).toBe(false)
    }
  })
})

describe("jobElapsedMs", () => {
  it("runs a live job up to now, from its start", () => {
    const now = Date.parse("2026-11-18T09:00:35.000Z")
    expect(jobElapsedMs(base, now)).toBe(30_000)
  })

  it("stops a finished job at its finish time", () => {
    const job = { ...base, state: "succeeded" as const, finished_at: "2026-11-18T09:01:05.000Z" }
    expect(jobElapsedMs(job, Date.parse("2026-11-18T12:00:00.000Z"))).toBe(60_000)
  })

  it("measures a queued job from its creation, since it has not started", () => {
    const queued = { ...base, state: "queued" as const, started_at: undefined }
    expect(jobElapsedMs(queued, Date.parse("2026-11-18T09:00:10.000Z"))).toBe(10_000)
  })

  it("returns null for a timestamp it cannot read", () => {
    expect(jobElapsedMs({ ...base, created_at: "not a time", started_at: undefined }, 0)).toBeNull()
  })
})

describe("formatDuration", () => {
  it("uses seconds, then minutes and seconds, then hours and minutes", () => {
    expect(formatDuration(0)).toBe("0s")
    expect(formatDuration(42_900)).toBe("42s")
    expect(formatDuration(185_000)).toBe("3m 05s")
    expect(formatDuration(3_720_000)).toBe("1h 02m")
  })
})
