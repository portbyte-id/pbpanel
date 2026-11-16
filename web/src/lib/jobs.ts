/**
 * The job output stream: `GET /api/jobs/{id}/events` as Server-Sent Events.
 *
 * The server sends each line with its sequence number as the SSE `id`, and
 * resumes from `Last-Event-ID` (or `?last_event_id=`) on reconnect. The browser's
 * EventSource reconnects by itself and replays `Last-Event-ID` — but only while
 * the connection is open. The server also *ends* a stream deliberately:
 *
 *  - `end` when the job reached a terminal state: nothing more is coming;
 *  - `dropped` when this subscriber fell behind: the client must reconnect from
 *    the last sequence it saw, or it would stop reading a live job;
 *  - `truncated` when the replay starts mid-stream: earlier output is gone.
 *
 * EventSource does not retry a stream the server closed cleanly, so `dropped`
 * is handled here by opening a fresh source at the last seen sequence.
 */

export interface JobLine {
  seq: number
  text: string
}

export interface JobEvent {
  state: string
  exit_code?: number
  error?: string
}

export type JobStreamStatus = "connecting" | "open" | "closed" | "error"

export interface JobStreamHandlers {
  onLine: (line: JobLine) => void
  onTruncated: (dropped: number) => void
  onEnd: (job: JobEvent) => void
  onStatus: (status: JobStreamStatus) => void
}

/** The subset of EventSource this module uses, so a test can supply a fake. */
export interface EventSourceLike {
  addEventListener: (type: string, listener: (event: MessageEvent<string>) => void) => void
  close: () => void
  readyState: number
}

export type EventSourceFactory = (url: string) => EventSourceLike

const CLOSED = 2

/**
 * Opens the stream and returns a handle that closes it. Resume is automatic:
 * `lastEventId` is passed to the first connection, and every reconnect after a
 * `dropped` event uses the last sequence actually delivered.
 */
export function openJobStream(
  jobId: string,
  handlers: JobStreamHandlers,
  createEventSource: EventSourceFactory = (url) => new EventSource(url),
): { close: () => void } {
  let source: EventSourceLike | null = null
  let lastSeq = 0
  let stopped = false

  const connect = (afterSeq: number) => {
    const query = afterSeq > 0 ? `?last_event_id=${afterSeq}` : ""
    const es = createEventSource(`/api/jobs/${encodeURIComponent(jobId)}/events${query}`)
    source = es
    handlers.onStatus("connecting")

    es.addEventListener("open", () => {
      if (!stopped) handlers.onStatus("open")
    })

    es.addEventListener("message", (event) => {
      if (stopped) return
      const seq = Number.parseInt(event.lastEventId, 10)
      if (Number.isFinite(seq)) lastSeq = seq
      handlers.onLine({ seq: Number.isFinite(seq) ? seq : lastSeq, text: event.data })
    })

    es.addEventListener("truncated", (event) => {
      if (stopped) return
      const payload = parseJSON<{ dropped?: number }>(event.data)
      handlers.onTruncated(payload?.dropped ?? 0)
    })

    // `dropped` means this subscriber fell behind and the server closed the
    // stream. Reconnect from the last line shown rather than waiting for a
    // browser retry that a clean close never triggers.
    es.addEventListener("dropped", () => {
      if (stopped) return
      es.close()
      connect(lastSeq)
    })

    es.addEventListener("end", (event) => {
      const payload = parseJSON<JobEvent>(event.data)
      if (!stopped && payload) handlers.onEnd(payload)
      stopped = true
      es.close()
      handlers.onStatus("closed")
    })

    es.addEventListener("error", () => {
      // A browser-managed retry stays in CONNECTING and will fire `open` again;
      // only a permanently closed source is reported as an error.
      if (!stopped && es.readyState === CLOSED) {
        handlers.onStatus("error")
        stopped = true
      }
    })
  }

  connect(0)

  return {
    close: () => {
      stopped = true
      source?.close()
      source = null
      handlers.onStatus("closed")
    },
  }
}

function parseJSON<T>(raw: string): T | null {
  try {
    return JSON.parse(raw) as T
  } catch {
    return null
  }
}
