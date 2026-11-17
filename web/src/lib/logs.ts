/**
 * The log viewer's client: the source list, the tail stream and the search.
 *
 * Both streams are read with `fetch` and a stream reader rather than
 * EventSource. A failure before the first line (an unknown id, a log file that
 * does not exist yet, Docker unreachable) is an ordinary JSON error, and an
 * EventSource hides its status and body; a reader keeps the server's sentence.
 */

import { ApiError, api } from "@/lib/api"

export type LogKind = "panel" | "audit" | "nginx" | "site" | "app" | "cron" | "docker"

export interface LogSource {
  id: string
  label: string
  kind: LogKind
}

export const KIND_LABELS: Record<LogKind, string> = {
  panel: "Panel",
  audit: "Audit",
  nginx: "Nginx",
  site: "Sites",
  app: "Apps",
  cron: "Cron jobs",
  docker: "Containers",
}

/** The order the groups are listed in: the panel's own logs first. */
const KIND_ORDER: LogKind[] = ["panel", "audit", "nginx", "docker", "site", "app", "cron"]

export interface LogGroup {
  kind: LogKind
  label: string
  sources: LogSource[]
}

/** Sources grouped by kind in a fixed order, each group sorted by label. A kind this build does not know gets its own group at the end. */
export function groupSources(sources: LogSource[]): LogGroup[] {
  const byKind = new Map<string, LogSource[]>()
  for (const s of sources) byKind.set(s.kind, [...(byKind.get(s.kind) ?? []), s])
  const kinds = [
    ...KIND_ORDER.filter((k) => byKind.has(k)),
    ...[...byKind.keys()].filter((k) => !(KIND_ORDER as string[]).includes(k)),
  ]
  return kinds.map((kind) => ({
    kind: kind as LogKind,
    label: KIND_LABELS[kind as LogKind] ?? kind,
    sources: [...(byKind.get(kind) ?? [])].sort((a, b) => a.label.localeCompare(b.label)),
  }))
}

/** What the server will not do: lines is capped at 5000. */
export const MAX_TAIL_LINES = 5000
export const TAIL_LINE_CHOICES = [100, 200, 500, 1000, 5000]

export type OpenStream = (path: string, accept: string, signal: AbortSignal) => Promise<Response>

const defaultOpen: OpenStream = (path, accept, signal) => api.openStream(path, accept, signal)

/** One decoded Server-Sent Event. */
export interface SSEEvent {
  event: string
  data: string
}

/**
 * An incremental parser for `text/event-stream`. Chunks can end anywhere, so
 * the unfinished tail is kept until its blank line arrives. Comment lines
 * (`: keep-alive`) are dropped.
 */
export class SSEParser {
  #buffer = ""

  push(chunk: string): SSEEvent[] {
    this.#buffer += chunk.replace(/\r\n?/g, "\n")
    const events: SSEEvent[] = []
    for (;;) {
      const end = this.#buffer.indexOf("\n\n")
      if (end === -1) break
      const block = this.#buffer.slice(0, end)
      this.#buffer = this.#buffer.slice(end + 2)
      let event = "message"
      const data: string[] = []
      for (const line of block.split("\n")) {
        if (line === "" || line.startsWith(":")) continue
        const colon = line.indexOf(":")
        const field = colon === -1 ? line : line.slice(0, colon)
        let value = colon === -1 ? "" : line.slice(colon + 1)
        if (value.startsWith(" ")) value = value.slice(1)
        if (field === "event") event = value
        else if (field === "data") data.push(value)
      }
      if (data.length > 0 || event !== "message") events.push({ event, data: data.join("\n") })
    }
    return events
  }
}

async function consume(body: ReadableStream<Uint8Array> | null, onText: (text: string) => void) {
  if (body === null) return
  const reader = body.getReader()
  const decoder = new TextDecoder()
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    onText(decoder.decode(value, { stream: true }))
  }
  const rest = decoder.decode()
  if (rest !== "") onText(rest)
}

function failureText(error: unknown, fallback: string): string {
  return error instanceof ApiError ? error.detail || fallback : fallback
}

function isAbort(error: unknown, signal: AbortSignal): boolean {
  return signal.aborted || (error instanceof DOMException && error.name === "AbortError")
}

export type TailStatus = "connecting" | "streaming" | "ended" | "failed"

export interface TailHandlers {
  onLine: (line: string) => void
  onStatus: (status: TailStatus) => void
  /** The server's sentence for a failure. Followed by `onStatus("failed")`. */
  onError: (message: string) => void
}

export interface TailOptions {
  lines: number
  follow: boolean
}

/** Starts a tail. `close` aborts the request, which is what stops the server too. */
export function openTail(
  id: string,
  options: TailOptions,
  handlers: TailHandlers,
  open: OpenStream = defaultOpen,
): { close: () => void } {
  const controller = new AbortController()
  const lines = Math.min(Math.max(Math.trunc(options.lines), 0), MAX_TAIL_LINES)
  const path = `/api/logs/${encodeURIComponent(id)}/tail?lines=${lines}&follow=${options.follow ? 1 : 0}`

  const run = async () => {
    handlers.onStatus("connecting")
    try {
      const res = await open(path, "text/event-stream", controller.signal)
      handlers.onStatus("streaming")
      const parser = new SSEParser()
      let ended = false
      let failed = false
      await consume(res.body, (text) => {
        for (const ev of parser.push(text)) {
          if (ev.event === "line") {
            const parsed = safeJSON<{ line?: string }>(ev.data)
            if (typeof parsed?.line === "string") handlers.onLine(parsed.line)
          } else if (ev.event === "end") {
            ended = true
          } else if (ev.event === "error") {
            failed = true
            handlers.onError(safeJSON<{ detail?: string }>(ev.data)?.detail || "The log stream failed.")
          }
        }
      })
      if (controller.signal.aborted) return
      if (failed) handlers.onStatus("failed")
      else if (ended || !options.follow) handlers.onStatus("ended")
      else {
        handlers.onError("The connection closed before the log ended.")
        handlers.onStatus("failed")
      }
    } catch (error) {
      if (isAbort(error, controller.signal)) return
      handlers.onError(failureText(error, "The log could not be read."))
      handlers.onStatus("failed")
    }
  }
  void run()

  return { close: () => controller.abort() }
}

export interface SearchParams {
  q: string
  regex: boolean
  caseInsensitive: boolean
  /** How far back to look, in uncompressed MB. */
  lastMB: number
  /** The match cap. */
  limit: number
}

export const DEFAULT_LAST_MB = 16
export const DEFAULT_LIMIT = 1000
export const MAX_LAST_MB = 1024
export const MAX_LIMIT = 10_000

export interface SearchMatch {
  file: string
  /** 1-based within the file, or within the scanned part when `partial`. */
  line: number
  /** The file was scanned from the middle, so `line` counts from the first scanned line. */
  partial: boolean
  text: string
}

export interface SearchStats {
  files: number
  bytesScanned: number
  matches: number
  limitHit: boolean
  windowCapped: boolean
}

export interface SearchHandlers {
  onMatch: (match: SearchMatch) => void
  onDone: (stats: SearchStats) => void
  /** A failure that came with or after the results; matches already delivered stay valid. */
  onError: (message: string, field: string | null) => void
}

export function searchPath(id: string, p: SearchParams): string {
  const q = new URLSearchParams({ q: p.q })
  if (p.regex) q.set("regex", "1")
  if (p.caseInsensitive) q.set("ci", "1")
  q.set("last_mb", String(p.lastMB))
  q.set("limit", String(p.limit))
  return `/api/logs/${encodeURIComponent(id)}/search?${q.toString()}`
}

interface SearchLine {
  type?: string
  file?: string
  line?: number
  partial?: boolean
  text?: string
  stats?: SearchStats
  error?: string
  detail?: string
}

/**
 * Runs a search and delivers each NDJSON line as it arrives. The stream ends
 * with exactly one `done` or one `error` line; a stream that stops without
 * either was cut off and is reported as such, because its match list is not
 * complete.
 */
export function runSearch(
  id: string,
  params: SearchParams,
  handlers: SearchHandlers,
  open: OpenStream = defaultOpen,
): { close: () => void } {
  const controller = new AbortController()

  const take = (raw: string): "done" | "error" | null => {
    if (raw.trim() === "") return null
    const row = safeJSON<SearchLine>(raw)
    if (row === null) return null
    if (row.type === "match") {
      handlers.onMatch({
        file: row.file ?? "",
        line: row.line ?? 0,
        partial: row.partial === true,
        text: row.text ?? "",
      })
      return null
    }
    if (row.type === "done" && row.stats) {
      handlers.onDone(row.stats)
      return "done"
    }
    if (row.type === "error") {
      handlers.onError(row.detail || "The search failed part way.", null)
      return "error"
    }
    return null
  }

  const run = async () => {
    try {
      const res = await open(searchPath(id, params), "application/x-ndjson", controller.signal)
      let rest = ""
      let outcome: "done" | "error" | null = null
      await consume(res.body, (text) => {
        rest += text
        for (;;) {
          const nl = rest.indexOf("\n")
          if (nl === -1) break
          const row = take(rest.slice(0, nl))
          rest = rest.slice(nl + 1)
          if (row !== null) outcome = row
        }
      })
      if (controller.signal.aborted) return
      if (rest.trim() !== "") {
        const row = take(rest)
        if (row !== null) outcome = row
      }
      if (outcome === null) handlers.onError("The search was cut off before it finished.", null)
    } catch (error) {
      if (isAbort(error, controller.signal)) return
      const field = error instanceof ApiError ? error.field : null
      handlers.onError(failureText(error, "The search could not be run."), field)
    }
  }
  void run()

  return { close: () => controller.abort() }
}

function safeJSON<T>(raw: string): T | null {
  try {
    return JSON.parse(raw) as T
  } catch {
    return null
  }
}
