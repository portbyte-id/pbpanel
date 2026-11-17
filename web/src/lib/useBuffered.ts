import { useCallback, useEffect, useRef, useState } from "react"

const FLUSH_MS = 50

/**
 * A list fed by a stream. A tail backlog is thousands of lines in a burst, and
 * one state update per line would render the page thousands of times; items are
 * collected and committed at most every 50 ms. `hold` freezes the committed
 * list while the stream keeps filling the buffer, which is what Pause means.
 * Only the newest `cap` items are kept.
 */
export function useBuffered<T>(cap: number) {
  const [items, setItems] = useState<T[]>([])
  const buffer = useRef<T[]>([])
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const held = useRef(false)

  const flush = useCallback(() => {
    clearTimeout(timer.current)
    timer.current = undefined
    if (held.current || buffer.current.length === 0) return
    const add = buffer.current
    buffer.current = []
    setItems((prev) => {
      const next = prev.concat(add)
      return next.length > cap ? next.slice(next.length - cap) : next
    })
  }, [cap])

  const push = useCallback(
    (item: T) => {
      buffer.current.push(item)
      if (buffer.current.length > cap) buffer.current.splice(0, buffer.current.length - cap)
      if (timer.current === undefined && !held.current) timer.current = setTimeout(flush, FLUSH_MS)
    },
    [cap, flush],
  )

  const clear = useCallback(() => {
    clearTimeout(timer.current)
    timer.current = undefined
    buffer.current = []
    setItems([])
  }, [])

  const hold = useCallback(
    (value: boolean) => {
      held.current = value
      if (!value) flush()
    },
    [flush],
  )

  useEffect(() => () => clearTimeout(timer.current), [])

  return { items, push, clear, hold, flush }
}
