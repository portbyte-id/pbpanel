import { describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api"
import {
  groupSources,
  openTail,
  runSearch,
  searchPath,
  SSEParser,
  type LogSource,
  type OpenStream,
  type SearchStats,
  type TailStatus,
} from "@/lib/logs"
import { streamOf } from "@/test/helpers"

describe("groupSources", () => {
  const src = (id: string, label: string, kind: LogSource["kind"]): LogSource => ({ id, label, kind })

  it("groups by kind in a fixed order and sorts each group by label", () => {
    const groups = groupSources([
      src("site:b.test:access", "b.test access", "site"),
      src("panel:audit", "Audit", "audit"),
      src("site:a.test:access", "a.test access", "site"),
      src("docker:panel-nginx", "nginx container", "docker"),
    ])
    expect(groups.map((g) => g.kind)).toEqual(["audit", "docker", "site"])
    expect(groups[2]?.sources.map((s) => s.label)).toEqual(["a.test access", "b.test access"])
    expect(groups[2]?.label).toBe("Sites")
  })
})

describe("SSEParser", () => {
  it("reassembles events split across chunks and drops comments", () => {
    const p = new SSEParser()
    expect(p.push(": keep-alive\n\nevent: line\ndata: {\"li")).toEqual([])
    expect(p.push('ne":"a"}\n\nevent: end\r\ndata: {}\r\n\r\n')).toEqual([
      { event: "line", data: '{"line":"a"}' },
      { event: "end", data: "{}" },
    ])
  })
})

describe("openTail", () => {
  const line = (s: string) => `event: line\ndata: ${JSON.stringify({ line: s })}\n\n`

  function collect() {
    const lines: string[] = []
    const statuses: TailStatus[] = []
    const errors: string[] = []
    return {
      lines,
      statuses,
      errors,
      handlers: {
        onLine: (l: string) => lines.push(l),
        onStatus: (s: TailStatus) => statuses.push(s),
        onError: (m: string) => errors.push(m),
      },
    }
  }

  it("asks for the lines and follow flag, delivers lines and ends on `end`", async () => {
    const c = collect()
    const open = vi.fn<OpenStream>(async () =>
      streamOf([line("one"), line("two"), "event: end\ndata: {}\n\n"]),
    )
    openTail("site:a.test:access", { lines: 50, follow: false }, c.handlers, open)
    await vi.waitFor(() => expect(c.statuses.at(-1)).toBe("ended"))
    expect(open.mock.calls[0]?.[0]).toBe("/api/logs/site%3Aa.test%3Aaccess/tail?lines=50&follow=0")
    expect(open.mock.calls[0]?.[1]).toBe("text/event-stream")
    expect(c.lines).toEqual(["one", "two"])
  })

  it("caps lines at 5000", () => {
    const open = vi.fn<OpenStream>(() => new Promise(() => undefined))
    openTail("x", { lines: 99_999, follow: true }, collect().handlers, open)
    expect(open.mock.calls[0]?.[0]).toBe("/api/logs/x/tail?lines=5000&follow=1")
  })

  it("keeps the server's sentence when the stream cannot start", async () => {
    const c = collect()
    const open: OpenStream = async () => {
      throw new ApiError(409, "source_unavailable", "That log file does not exist yet.")
    }
    openTail("x", { lines: 10, follow: true }, c.handlers, open)
    await vi.waitFor(() => expect(c.statuses.at(-1)).toBe("failed"))
    expect(c.errors).toEqual(["That log file does not exist yet."])
  })

  it("reports an error event after lines, and a followed stream that just stops", async () => {
    const failed = collect()
    openTail(
      "x",
      { lines: 10, follow: true },
      failed.handlers,
      async () =>
        streamOf([line("a"), 'event: error\ndata: {"error":"internal_error","detail":"disk went away"}\n\n']),
    )
    await vi.waitFor(() => expect(failed.statuses.at(-1)).toBe("failed"))
    expect(failed.lines).toEqual(["a"])
    expect(failed.errors).toEqual(["disk went away"])

    const cut = collect()
    openTail("x", { lines: 10, follow: true }, cut.handlers, async () => streamOf([line("a")]))
    await vi.waitFor(() => expect(cut.statuses.at(-1)).toBe("failed"))
    expect(cut.errors[0]).toMatch(/closed/)
  })

  it("stays silent after close", async () => {
    const c = collect()
    let release: () => void = () => undefined
    const open: OpenStream = (_p, _a, signal) =>
      new Promise((_resolve, reject) => {
        release = () => reject(new DOMException("aborted", "AbortError"))
        signal.addEventListener("abort", release)
      })
    const handle = openTail("x", { lines: 10, follow: true }, c.handlers, open)
    handle.close()
    await Promise.resolve()
    expect(c.statuses).toEqual(["connecting"])
    expect(c.errors).toEqual([])
  })
})

describe("runSearch", () => {
  const params = { q: "GET /", regex: true, caseInsensitive: true, lastMB: 4, limit: 500 }

  it("builds the query string from the toggles", () => {
    expect(searchPath("site:a.test:access", params)).toBe(
      "/api/logs/site%3Aa.test%3Aaccess/search?q=GET+%2F&regex=1&ci=1&last_mb=4&limit=500",
    )
    expect(searchPath("x", { ...params, regex: false, caseInsensitive: false })).toBe(
      "/api/logs/x/search?q=GET+%2F&last_mb=4&limit=500",
    )
  })

  it("streams matches, handling a line split across chunks, then the stats", async () => {
    const stats: SearchStats = { files: 2, bytesScanned: 1024, matches: 2, limitHit: true, windowCapped: false }
    const matches: string[] = []
    const done = vi.fn()
    const err = vi.fn()
    const rows = [
      '{"type":"match","file":"a.log.1","line":42,"partial":true,"text":"first"}\n{"type":"mat',
      'ch","file":"a.log","line":3,"text":"second"}\n',
      `${JSON.stringify({ type: "done", stats })}\n`,
    ]
    runSearch(
      "x",
      params,
      { onMatch: (m) => matches.push(`${m.file}:${m.line}:${m.partial}:${m.text}`), onDone: done, onError: err },
      async () => streamOf(rows),
    )
    await vi.waitFor(() => expect(done).toHaveBeenCalled())
    expect(matches).toEqual(["a.log.1:42:true:first", "a.log:3:false:second"])
    expect(done).toHaveBeenCalledWith(stats)
    expect(err).not.toHaveBeenCalled()
  })

  it("reports an error line after matches and a stream cut off with no end line", async () => {
    const m1 = vi.fn()
    const e1 = vi.fn()
    runSearch(
      "x",
      params,
      { onMatch: m1, onDone: vi.fn(), onError: e1 },
      async () =>
        streamOf([
          '{"type":"match","file":"a","line":1,"text":"t"}\n{"type":"error","error":"internal_error","detail":"read failed"}\n',
        ]),
    )
    await vi.waitFor(() => expect(e1).toHaveBeenCalledWith("read failed", null))
    expect(m1).toHaveBeenCalledTimes(1)

    const e2 = vi.fn()
    runSearch(
      "x",
      params,
      { onMatch: vi.fn(), onDone: vi.fn(), onError: e2 },
      async () => streamOf(['{"type":"match","file":"a","line":1,"text":"t"}\n']),
    )
    await vi.waitFor(() => expect(e2).toHaveBeenCalled())
    expect(e2.mock.calls[0]?.[0]).toMatch(/cut off/)
  })

  it("passes the rejected field of a 400 through", async () => {
    const err = vi.fn()
    runSearch("x", params, { onMatch: vi.fn(), onDone: vi.fn(), onError: err }, async () => {
      throw new ApiError(400, "invalid_input", "q: not a valid expression", { field: "q" })
    })
    await vi.waitFor(() => expect(err).toHaveBeenCalledWith("q: not a valid expression", "q"))
  })
})
