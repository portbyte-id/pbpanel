import { useEffect, useState } from "react"

/**
 * Whole seconds left until `deadline`, rounded up so the display shows 1 until
 * the instant the deadline passes, and never negative. An unparseable deadline
 * counts as already expired: the safe reading for a keep-or-rollback timer.
 */
export function secondsRemaining(deadline: string, now: number): number {
  const at = Date.parse(deadline)
  if (Number.isNaN(at)) return 0
  return Math.max(0, Math.ceil((at - now) / 1000))
}

/**
 * Seconds as a screen reader hears them: rounded up to a 15 second step while
 * far from the deadline, exact for the last ten. A live region that changed
 * every second would be read out every second.
 */
export function coarseSeconds(seconds: number): number {
  if (seconds <= 10) return seconds
  return Math.ceil(seconds / 15) * 15
}

/**
 * Counts down to a server deadline. Every tick recomputes from `Date.now()`
 * against the server's timestamp, so a throttled background tab or a pending
 * object that arrives mid-countdown shows the true time, not a drifted counter.
 * A new deadline restarts the clock; `null` means nothing is pending.
 */
export function useCountdown(deadline: string | null): { seconds: number; expired: boolean } {
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    if (deadline === null) return
    setNow(Date.now())
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [deadline])

  if (deadline === null) return { seconds: 0, expired: false }
  const seconds = secondsRemaining(deadline, now)
  return { seconds, expired: seconds === 0 }
}
