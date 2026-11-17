import { describe, expect, it, vi } from "vitest"

import {
  formatBytes,
  formatPercent,
  formatRate,
  formatUptime,
  mergeSnapshots,
  openSysinfoStream,
  ratioPercent,
  sparkPoints,
  type EventSourceLike,
} from "@/lib/sysinfo"
import { snap } from "@/test/helpers"

describe("mergeSnapshots", () => {
  it("keeps one snapshot per time, oldest first, newest cap kept", () => {
    const base = [snap("2026-11-17T10:00:00Z"), snap("2026-11-17T10:00:05Z")]
    const extra = [snap("2026-11-17T10:00:05Z", { cpu_percent: 99 }), snap("2026-11-17T10:00:10Z")]
    const merged = mergeSnapshots(base, extra)
    expect(merged.map((s) => s.time)).toEqual([
      "2026-11-17T10:00:00Z",
      "2026-11-17T10:00:05Z",
      "2026-11-17T10:00:10Z",
    ])
    // The streamed copy of a repeated sample wins.
    expect(merged[1]?.cpu_percent).toBe(99)
    expect(mergeSnapshots(base, extra, 2).map((s) => s.time)).toEqual([
      "2026-11-17T10:00:05Z",
      "2026-11-17T10:00:10Z",
    ])
  })
})

describe("formatters", () => {
  it("formats bytes, rates, percentages and uptime", () => {
    expect(formatBytes(0)).toBe("0 B")
    expect(formatBytes(1536)).toBe("1.50 KiB")
    expect(formatBytes(8 * 1024 ** 3)).toBe("8.00 GiB")
    expect(formatRate(2048)).toBe("2.00 KiB/s")
    expect(formatPercent(7.26)).toBe("7.3%")
    expect(formatPercent(42.4)).toBe("42%")
    expect(formatUptime(3 * 86_400 + 4 * 3_600 + 30)).toBe("3d 4h")
    expect(formatUptime(5 * 3_600 + 12 * 60)).toBe("5h 12m")
    expect(formatUptime(420)).toBe("7m")
    expect(ratioPercent(1, 0)).toBe(0)
    expect(ratioPercent(1, 4)).toBe(25)
  })
})

describe("sparkPoints", () => {
  it("scales to the ceiling and puts zero on the baseline", () => {
    expect(sparkPoints([0, 50, 100], 100, 10, 100)).toBe("0.0,10.0 50.0,5.0 100.0,0.0")
    expect(sparkPoints([0, 0], 10, 10)).toBe("0.0,10.0 10.0,10.0")
    expect(sparkPoints([], 10, 10)).toBe("")
  })
})

function fakeSource() {
  const listeners = new Map<string, (e: MessageEvent<string>) => void>()
  const source: EventSourceLike & { closed: boolean } = {
    closed: false,
    addEventListener: (type, l) => void listeners.set(type, l),
    close: () => {
      source.closed = true
    },
  }
  const emit = (type: string, data = "") => listeners.get(type)?.({ data } as MessageEvent<string>)
  return { source, emit }
}

describe("openSysinfoStream", () => {
  it("reports polling when EventSource does not exist", () => {
    const status = vi.fn()
    // jsdom has no EventSource, which is the case under test.
    openSysinfoStream({ onSnapshot: vi.fn(), onStatus: status })
    expect(status).toHaveBeenCalledWith("polling")
  })

  it("delivers snapshots, ignores a malformed frame, and falls back to polling on error", () => {
    vi.useFakeTimers()
    const fake = fakeSource()
    const onSnapshot = vi.fn()
    const onStatus = vi.fn()
    const handle = openSysinfoStream({ onSnapshot, onStatus }, () => fake.source)

    fake.emit("snapshot", "not json")
    expect(onSnapshot).not.toHaveBeenCalled()
    fake.emit("snapshot", JSON.stringify(snap("2026-11-17T10:00:00Z")))
    expect(onSnapshot).toHaveBeenCalledTimes(1)
    expect(onStatus).toHaveBeenLastCalledWith("live")

    fake.emit("error")
    expect(fake.source.closed).toBe(true)
    expect(onStatus).toHaveBeenLastCalledWith("polling")

    handle.close()
    vi.useRealTimers()
  })
})
