import { act, renderHook } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { coarseSeconds, secondsRemaining, useCountdown } from "@/lib/countdown"

const T0 = Date.parse("2026-11-17T12:00:00Z")
const at = (offsetSeconds: number) => new Date(T0 + offsetSeconds * 1000).toISOString()

describe("secondsRemaining", () => {
  it("rounds up and never goes negative", () => {
    expect(secondsRemaining(at(60), T0)).toBe(60)
    expect(secondsRemaining(at(60), T0 + 500)).toBe(60)
    expect(secondsRemaining(at(60), T0 + 1000)).toBe(59)
    expect(secondsRemaining(at(0), T0)).toBe(0)
    expect(secondsRemaining(at(-30), T0)).toBe(0)
  })

  it("treats an unparseable deadline as expired", () => {
    expect(secondsRemaining("soon", T0)).toBe(0)
  })
})

describe("coarseSeconds", () => {
  it("is exact for the last ten seconds and steps by 15 above", () => {
    expect(coarseSeconds(10)).toBe(10)
    expect(coarseSeconds(3)).toBe(3)
    expect(coarseSeconds(11)).toBe(15)
    expect(coarseSeconds(45)).toBe(45)
    expect(coarseSeconds(46)).toBe(60)
  })
})

describe("useCountdown", () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(T0)
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it("ticks once a second", () => {
    const { result } = renderHook(() => useCountdown(at(60)))
    expect(result.current).toEqual({ seconds: 60, expired: false })

    act(() => void vi.advanceTimersByTime(1000))
    expect(result.current.seconds).toBe(59)

    act(() => void vi.advanceTimersByTime(4000))
    expect(result.current.seconds).toBe(55)
  })

  it("reaches zero, reports expired and stays there", () => {
    const { result } = renderHook(() => useCountdown(at(3)))

    act(() => void vi.advanceTimersByTime(3000))
    expect(result.current).toEqual({ seconds: 0, expired: true })

    act(() => void vi.advanceTimersByTime(5000))
    expect(result.current).toEqual({ seconds: 0, expired: true })
  })

  it("is expired at once when the deadline is already past", () => {
    const { result } = renderHook(() => useCountdown(at(-10)))
    expect(result.current).toEqual({ seconds: 0, expired: true })
  })

  it("restarts from the new deadline when a new pending replaces the old", () => {
    const { result, rerender } = renderHook(({ d }) => useCountdown(d), {
      initialProps: { d: at(20) as string | null },
    })
    act(() => void vi.advanceTimersByTime(5000))
    expect(result.current.seconds).toBe(15)

    rerender({ d: at(80) })
    expect(result.current.seconds).toBe(75)
    act(() => void vi.advanceTimersByTime(1000))
    expect(result.current.seconds).toBe(74)
  })

  it("starts correctly when the pending object arrives mid-countdown", () => {
    const { result, rerender } = renderHook(({ d }) => useCountdown(d), {
      initialProps: { d: null as string | null },
    })
    expect(result.current).toEqual({ seconds: 0, expired: false })

    // The page learns of the timer only after 7 s; the deadline is absolute.
    act(() => void vi.advanceTimersByTime(7000))
    rerender({ d: new Date(Date.now() + 40_000).toISOString() })
    expect(result.current.seconds).toBe(40)
    act(() => void vi.advanceTimersByTime(2000))
    expect(result.current.seconds).toBe(38)
  })

  it("is idle with no deadline and stops ticking on unmount", () => {
    const { result, rerender, unmount } = renderHook(({ d }) => useCountdown(d), {
      initialProps: { d: at(30) as string | null },
    })
    rerender({ d: null })
    expect(result.current).toEqual({ seconds: 0, expired: false })
    unmount()
    expect(vi.getTimerCount()).toBe(0)
  })
})
