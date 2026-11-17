import { useEffect, useRef, useState } from "react"

import { api, type LogChunk } from "@/lib/api"

/** How much of the log the view keeps. The file on disk is the record. */
const MAX_CHARS = 200_000
const RECONNECT_MS = 2_000
const POLL_MS = 2_000

/** Appends a chunk and keeps the last MAX_CHARS characters. */
export function appendLog(text: string, chunk: string): string {
  const next = text + chunk
  return next.length > MAX_CHARS ? next.slice(next.length - MAX_CHARS) : next
}

/**
 * An app's log, followed live. It reads the server-sent stream and, when the
 * connection drops, reconnects from the offset of the last chunk so a gap is
 * not replayed as duplicate lines. Where EventSource does not exist it polls.
 */
export function AppLogs({ name }: { name: string }) {
  const [text, setText] = useState("")
  const [status, setStatus] = useState<"connecting" | "live" | "reconnecting">("connecting")
  const view = useRef<HTMLPreElement>(null)

  useEffect(() => {
    let stopped = false
    let source: EventSource | null = null
    let timer: ReturnType<typeof setTimeout> | undefined
    let offset: number | undefined

    const take = (c: LogChunk) => {
      offset = c.offset
      if (c.data !== "") setText((t) => appendLog(t, c.data))
      setStatus("live")
    }

    const poll = async () => {
      try {
        take(await api.appLog(name, offset))
      } catch {
        setStatus("reconnecting")
      }
      if (!stopped) timer = setTimeout(() => void poll(), POLL_MS)
    }

    const connect = () => {
      const q = offset === undefined ? "" : `?offset=${offset}`
      source = new EventSource(`/api/apps/${encodeURIComponent(name)}/logs/stream${q}`)
      source.addEventListener("log", (e) => take(JSON.parse((e as MessageEvent<string>).data) as LogChunk))
      source.onerror = () => {
        // EventSource would reconnect on its own but without the offset.
        source?.close()
        setStatus("reconnecting")
        if (!stopped) timer = setTimeout(connect, RECONNECT_MS)
      }
    }

    if (typeof EventSource === "undefined") void poll()
    else connect()

    return () => {
      stopped = true
      source?.close()
      clearTimeout(timer)
    }
  }, [name])

  // Stay at the bottom while new lines arrive.
  useEffect(() => {
    const el = view.current
    if (el) el.scrollTop = el.scrollHeight
  }, [text])

  return (
    <div className="flex flex-col gap-2">
      <pre
        ref={view}
        tabIndex={0}
        aria-label={`Log of ${name}`}
        className="bg-muted h-96 overflow-auto rounded-md p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap"
      >
        {text === "" ? "No output yet." : text}
      </pre>
      <p className="text-muted-foreground text-xs" aria-live="polite">
        {status === "live" ? "Following the log." : status === "connecting" ? "Connecting…" : "Reconnecting…"}
      </p>
    </div>
  )
}
