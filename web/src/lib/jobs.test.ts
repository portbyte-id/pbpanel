import { describe, expect, it, vi } from "vitest"

import { openJobStream, type EventSourceLike, type JobLine } from "@/lib/jobs"

/**
 * A stand-in for EventSource: it records the URLs it was opened with and lets a
 * test fire the named events the server sends.
 */
class FakeEventSource implements EventSourceLike {
  static instances: FakeEventSource[] = []

  readonly url: string
  readyState = 1
  closed = false
  #listeners = new Map<string, ((event: MessageEvent<string>) => void)[]>()

  constructor(url: string) {
    this.url = url
    FakeEventSource.instances.push(this)
  }

  addEventListener(type: string, listener: (event: MessageEvent<string>) => void): void {
    const list = this.#listeners.get(type) ?? []
    list.push(listener)
    this.#listeners.set(type, list)
  }

  close(): void {
    this.closed = true
    this.readyState = 2
  }

  fire(type: string, data: string, lastEventId = ""): void {
    for (const listener of this.#listeners.get(type) ?? []) {
      listener({ data, lastEventId } as MessageEvent<string>)
    }
  }
}

function setup() {
  FakeEventSource.instances = []
  const lines: JobLine[] = []
  const ends: unknown[] = []
  const truncated: number[] = []
  const statuses: string[] = []

  const stream = openJobStream("job-1", {
    onLine: (line) => lines.push(line),
    onEnd: (job) => ends.push(job),
    onTruncated: (dropped) => truncated.push(dropped),
    onStatus: (status) => statuses.push(status),
  }, (url) => new FakeEventSource(url))

  return { stream, lines, ends, truncated, statuses }
}

describe("job event stream", () => {
  it("opens the job's events URL with no resume parameter", () => {
    setup()
    expect(FakeEventSource.instances).toHaveLength(1)
    expect(FakeEventSource.instances[0].url).toBe("/api/jobs/job-1/events")
  })

  it("reports each line with the sequence number from the SSE id", () => {
    const { lines } = setup()
    const es = FakeEventSource.instances[0]

    es.fire("message", "first", "1")
    es.fire("message", "second", "2")

    expect(lines).toEqual([
      { seq: 1, text: "first" },
      { seq: 2, text: "second" },
    ])
  })

  it("reports truncation so the drawer can say output was dropped", () => {
    const { truncated } = setup()
    FakeEventSource.instances[0].fire("truncated", JSON.stringify({ dropped: 120, last_seq: 130 }))

    expect(truncated).toEqual([120])
  })

  it("closes on end and reports the terminal state", () => {
    const { ends, statuses } = setup()
    const es = FakeEventSource.instances[0]

    es.fire("end", JSON.stringify({ state: "succeeded", exit_code: 0 }))

    expect(ends).toEqual([{ state: "succeeded", exit_code: 0 }])
    expect(es.closed).toBe(true)
    expect(statuses.at(-1)).toBe("closed")
  })

  // The server ends the stream when this subscriber falls behind, and
  // EventSource does not retry a clean close: the client must reconnect itself
  // from the last line it actually showed.
  it("resumes from the last seen sequence after a dropped event", () => {
    const { lines } = setup()
    const first = FakeEventSource.instances[0]

    first.fire("message", "one", "1")
    first.fire("message", "two", "2")
    first.fire("dropped", JSON.stringify({ last_seq: 2 }))

    expect(first.closed).toBe(true)
    expect(FakeEventSource.instances).toHaveLength(2)
    expect(FakeEventSource.instances[1].url).toBe("/api/jobs/job-1/events?last_event_id=2")

    FakeEventSource.instances[1].fire("message", "three", "3")
    expect(lines.map((l) => l.text)).toEqual(["one", "two", "three"])
  })

  it("closes the source when the caller closes the stream", () => {
    const { stream, statuses } = setup()
    const es = FakeEventSource.instances[0]

    stream.close()

    expect(es.closed).toBe(true)
    expect(statuses.at(-1)).toBe("closed")
  })

  it("does not report lines after close", () => {
    const { stream, lines } = setup()
    const es = FakeEventSource.instances[0]

    stream.close()
    es.fire("message", "late", "9")

    expect(lines).toEqual([])
  })

  it("reports an error only when the source is permanently closed", () => {
    const { statuses } = setup()
    const es = FakeEventSource.instances[0]

    // A transient error leaves the source CONNECTING and the browser retries.
    es.fire("error", "")
    expect(statuses).not.toContain("error")

    es.readyState = 2
    es.fire("error", "")
    expect(statuses.at(-1)).toBe("error")
  })

  it("encodes the job id in the URL", () => {
    FakeEventSource.instances = []
    openJobStream("a/b c", {
      onLine: vi.fn(),
      onEnd: vi.fn(),
      onTruncated: vi.fn(),
      onStatus: vi.fn(),
    }, (url) => new FakeEventSource(url))

    expect(FakeEventSource.instances[0].url).toBe("/api/jobs/a%2Fb%20c/events")
  })
})
