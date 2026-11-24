import { ErrorState } from "@/components/ErrorState"
import { SkeletonRows } from "@/components/ui/skeleton"
import { useEffect, useId, useMemo, useRef, useState } from "react"

import { WafEvents } from "@/components/WafEvents"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select } from "@/components/ui/select"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  DEFAULT_LAST_MB,
  DEFAULT_LIMIT,
  groupSources,
  MAX_TAIL_LINES,
  openTail,
  runSearch,
  TAIL_LINE_CHOICES,
  type LogSource,
  type SearchMatch,
  type SearchStats,
  type TailStatus,
} from "@/lib/logs"
import { useLogSources } from "@/lib/queries"
import { formatBytes } from "@/lib/sysinfo"
import { useBuffered } from "@/lib/useBuffered"

/** The most lines the viewer holds; the server caps one tail at 5000, a followed one keeps adding. */
const MAX_VIEW_LINES = 10_000
/** Matches shown per page of a search; the whole list is kept and paged through. */
const RESULT_PAGE_SIZE = 100

const LAST_MB_CHOICES = [1, 4, 16, 64, 256, 1024]
const LIMIT_CHOICES = [100, 1000, 5000, 10_000]

/**
 * The log files, and beside them the WAF audit: the same log pages, plus the
 * ModSecurity events as a table instead of raw lines.
 */
export function LogsPage() {
  return (
    <Tabs defaultValue="files">
      <TabsList aria-label="Log views">
        <TabsTrigger value="files">Log files</TabsTrigger>
        <TabsTrigger value="waf">WAF audit</TabsTrigger>
      </TabsList>
      <TabsContent value="files" className="pt-2">
        <LogFiles />
      </TabsContent>
      <TabsContent value="waf" className="pt-2">
        <WafEvents />
      </TabsContent>
    </Tabs>
  )
}

/** Source list, then a tail viewer and a search for the chosen source. */
function LogFiles() {
  const sources = useLogSources()
  const [selected, setSelected] = useState<LogSource | null>(null)

  if (sources.isPending) {
    return <SkeletonRows label="Loading log sources…" />
  }
  if (sources.error) {
    return <ErrorState error={sources.error} fallback="The log sources could not be loaded." onRetry={() => void sources.refetch()} />
  }

  return (
    <div className="grid gap-6 lg:grid-cols-[21rem_minmax(0,1fr)]">
      <SourceList sources={sources.data} selected={selected} onSelect={setSelected} />
      <div className="min-w-0">
        {selected === null ? (
          <p className="text-muted-foreground border-border rounded-md border border-dashed p-6 text-sm">
            Choose a log on the left to read its end or search it.
          </p>
        ) : (
          <LogViewer key={selected.id} source={selected} />
        )}
      </div>
    </div>
  )
}

/* --- Sources ------------------------------------------------------------ */

export function SourceList({
  sources,
  selected,
  onSelect,
}: {
  sources: LogSource[]
  selected: LogSource | null
  onSelect: (source: LogSource) => void
}) {
  const filterId = useId()
  const [filter, setFilter] = useState("")

  const groups = useMemo(() => {
    const needle = filter.trim().toLowerCase()
    const shown =
      needle === ""
        ? sources
        : sources.filter(
            (s) => s.label.toLowerCase().includes(needle) || s.id.toLowerCase().includes(needle),
          )
    return groupSources(shown)
  }, [sources, filter])

  return (
    <nav aria-label="Log sources" className="flex flex-col gap-3">
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={filterId}>Filter logs</Label>
        <Input
          id={filterId}
          type="search"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Name or kind"
          autoComplete="off"
        />
      </div>

      {sources.length === 0 ? (
        <p className="text-muted-foreground text-sm">No log sources are available yet.</p>
      ) : groups.length === 0 ? (
        <p className="text-muted-foreground text-sm">No log matches “{filter}”.</p>
      ) : (
        groups.map((g) => (
          <section key={g.kind} aria-label={g.label} className="flex flex-col gap-1">
            <h3 className="text-muted-foreground px-2 text-xs font-medium">{g.label}</h3>
            <ul className="flex flex-col">
              {g.sources.map((s) => (
                <li key={s.id}>
                  <button
                    type="button"
                    aria-current={selected?.id === s.id ? "true" : undefined}
                    onClick={() => onSelect(s)}
                    className="hover:bg-accent hover:text-accent-foreground aria-[current=true]:bg-accent aria-[current=true]:font-medium focus-visible:ring-ring/50 w-full rounded-md px-2 py-1.5 text-left text-sm break-words outline-none focus-visible:ring-[3px]"
                  >
                    {s.label}
                  </button>
                </li>
              ))}
            </ul>
          </section>
        ))
      )}
    </nav>
  )
}

/* --- Viewer ------------------------------------------------------------- */

function LogViewer({ source }: { source: LogSource }) {
  return (
    <section aria-label={source.label} className="flex flex-col gap-3">
      <h2 className="text-base font-semibold break-words">{source.label}</h2>
      <Tabs defaultValue="tail">
        <TabsList>
          <TabsTrigger value="tail">Tail</TabsTrigger>
          <TabsTrigger value="search">Search</TabsTrigger>
        </TabsList>
        <TabsContent value="tail">
          <TailViewer source={source} />
        </TabsContent>
        <TabsContent value="search">
          <SearchPanel source={source} />
        </TabsContent>
      </Tabs>
    </section>
  )
}

function tailNote(status: TailStatus, follow: boolean, paused: boolean, count: number): string {
  if (paused) return "Paused. New lines are held and appear when you resume."
  switch (status) {
    case "connecting":
      return "Connecting…"
    case "streaming":
      return follow ? "Following the log." : "Reading the log…"
    case "ended":
      return follow ? "The log stream ended." : `Showing the last ${count} lines.`
    case "failed":
      return "Stopped."
  }
}

export function TailViewer({ source }: { source: LogSource }) {
  const id = useId()
  const [lineCount, setLineCount] = useState(200)
  const [follow, setFollow] = useState(true)
  const [paused, setPaused] = useState(false)
  const [status, setStatus] = useState<TailStatus>("connecting")
  const [error, setError] = useState<string | null>(null)
  const [attempt, setAttempt] = useState(0)
  const { items, push, clear, hold, flush } = useBuffered<string>(MAX_VIEW_LINES)
  const view = useRef<HTMLPreElement>(null)

  useEffect(() => {
    clear()
    setError(null)
    const tail = openTail(
      source.id,
      { lines: lineCount, follow },
      {
        onLine: push,
        onStatus: (s) => {
          setStatus(s)
          if (s === "ended" || s === "failed") flush()
        },
        onError: setError,
      },
    )
    return () => tail.close()
  }, [source.id, lineCount, follow, attempt, push, clear, flush])

  // Stay at the end while lines arrive; Pause is how the operator stops that.
  useEffect(() => {
    const el = view.current
    if (el && !paused) el.scrollTop = el.scrollHeight
  }, [items, paused])

  const togglePause = () => {
    const next = !paused
    setPaused(next)
    hold(next)
  }

  const text = items.join("\n")
  const live = status === "streaming" && follow

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-lines`}>Lines</Label>
          <Select
            id={`${id}-lines`}
            className="w-28"
            value={lineCount}
            onChange={(e) => setLineCount(Number(e.target.value))}
          >
            {TAIL_LINE_CHOICES.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </Select>
        </div>
        <Button
          type="button"
          variant={follow ? "default" : "outline"}
          aria-pressed={follow}
          onClick={() => setFollow((f) => !f)}
        >
          Follow
        </Button>
        <Button
          type="button"
          variant="outline"
          aria-pressed={paused}
          disabled={!live && !paused}
          onClick={togglePause}
        >
          {paused ? "Resume" : "Pause"}
        </Button>
        <Button type="button" variant="outline" onClick={clear}>
          Clear
        </Button>
        {status === "failed" && (
          <Button type="button" variant="outline" onClick={() => setAttempt((a) => a + 1)}>
            Reconnect
          </Button>
        )}
      </div>

      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}

      <pre
        ref={view}
        tabIndex={0}
        aria-label={`Log of ${source.label}`}
        className="bg-muted h-[35rem] overflow-auto rounded-md p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all"
      >
        {text !== ""
          ? text
          : status === "ended"
            ? "The log is empty."
            : status === "failed"
              ? ""
              : "No lines yet."}
      </pre>
      <p className="text-muted-foreground text-xs" aria-live="polite">
        {tailNote(status, follow, paused, items.length)}
        {items.length >= MAX_VIEW_LINES && ` Only the newest ${MAX_VIEW_LINES} lines are kept.`}
      </p>
      {lineCount >= MAX_TAIL_LINES && (
        <p className="text-muted-foreground text-xs">5000 lines is the most the server sends at once.</p>
      )}
    </div>
  )
}

/* --- Search ------------------------------------------------------------- */

type SearchState =
  | { phase: "idle" }
  | { phase: "running" }
  | { phase: "done"; stats: SearchStats }
  | { phase: "failed"; message: string; field: string | null }

/** What the finished search did and did not look at. */
export function SearchSummary({
  stats,
  limit,
  lastMB,
}: {
  stats: SearchStats
  limit: number
  lastMB: number
}) {
  return (
    <div className="flex flex-col gap-1 text-sm" role="status">
      <p>
        {stats.matches} {stats.matches === 1 ? "match" : "matches"} in {stats.files}{" "}
        {stats.files === 1 ? "file" : "files"}, {formatBytes(stats.bytesScanned)} scanned.
      </p>
      {stats.limitHit && (
        <p className="text-warning">
          Stopped at the limit of {limit} matches, so there may be more. Raise the limit or narrow the
          search.
        </p>
      )}
      {stats.windowCapped && (
        <p className="text-warning">
          Only the last {lastMB} MB was searched; older log content was not looked at.
        </p>
      )}
    </div>
  )
}

export function SearchPanel({ source }: { source: LogSource }) {
  const id = useId()
  const [q, setQ] = useState("")
  const [regex, setRegex] = useState(false)
  const [ci, setCi] = useState(false)
  const [lastMB, setLastMB] = useState(DEFAULT_LAST_MB)
  const [limit, setLimit] = useState(DEFAULT_LIMIT)
  const [state, setState] = useState<SearchState>({ phase: "idle" })
  const [resultPage, setResultPage] = useState(0)
  const [ran, setRan] = useState<{ limit: number; lastMB: number }>({
    limit: DEFAULT_LIMIT,
    lastMB: DEFAULT_LAST_MB,
  })
  const { items: matches, push, clear, flush } = useBuffered<SearchMatch>(10_000)
  const running = useRef<{ close: () => void } | null>(null)

  // Leaving the page (or the source) stops a search that is still reading.
  useEffect(() => () => running.current?.close(), [])

  const start = (e: React.FormEvent) => {
    e.preventDefault()
    running.current?.close()
    clear()
    setRan({ limit, lastMB })
    setResultPage(0)
    setState({ phase: "running" })
    running.current = runSearch(
      source.id,
      { q, regex, caseInsensitive: ci, lastMB, limit },
      {
        onMatch: push,
        onDone: (stats) => {
          flush()
          setState({ phase: "done", stats })
        },
        onError: (message, field) => {
          flush()
          setState({ phase: "failed", message, field })
        },
      },
    )
  }

  const cancel = () => {
    running.current?.close()
    running.current = null
    flush()
    setState({ phase: "idle" })
  }

  const isRunning = state.phase === "running"
  const qError = state.phase === "failed" && state.field === "q" ? state.message : null
  const otherError = state.phase === "failed" && state.field !== "q" ? state.message : null
  const pageCount = Math.max(1, Math.ceil(matches.length / RESULT_PAGE_SIZE))
  const currentPage = Math.min(resultPage, pageCount - 1)
  const firstRow = currentPage * RESULT_PAGE_SIZE
  const shown = matches.slice(firstRow, firstRow + RESULT_PAGE_SIZE)

  return (
    <div className="flex flex-col gap-4">
      <form onSubmit={start} className="flex flex-col gap-3" noValidate>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-q`}>Search for</Label>
          <Input
            id={`${id}-q`}
            value={q}
            onChange={(e) => setQ(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
            aria-invalid={qError !== null}
            aria-describedby={qError ? `${id}-q-error` : undefined}
          />
          {qError && (
            <p id={`${id}-q-error`} role="alert" className="text-destructive text-xs">
              {qError}
            </p>
          )}
        </div>

        <div className="flex flex-wrap items-end gap-x-6 gap-y-3">
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={regex} onChange={(e) => setRegex(e.target.checked)} />
            Regular expression
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={ci} onChange={(e) => setCi(e.target.checked)} />
            Ignore case
          </label>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-mb`}>Look back</Label>
            <Select
              id={`${id}-mb`}
              className="w-32"
              value={lastMB}
              onChange={(e) => setLastMB(Number(e.target.value))}
            >
              {LAST_MB_CHOICES.map((n) => (
                <option key={n} value={n}>
                  Last {n} MB
                </option>
              ))}
            </Select>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-limit`}>Stop after</Label>
            <Select
              id={`${id}-limit`}
              className="w-36"
              value={limit}
              onChange={(e) => setLimit(Number(e.target.value))}
            >
              {LIMIT_CHOICES.map((n) => (
                <option key={n} value={n}>
                  {n} matches
                </option>
              ))}
            </Select>
          </div>
          {isRunning ? (
            <Button type="button" variant="outline" onClick={cancel}>
              Stop search
            </Button>
          ) : (
            <Button type="submit" disabled={q === ""}>
              Search
            </Button>
          )}
        </div>
        {regex && (
          <p className="text-muted-foreground text-xs">
            Patterns use RE2 syntax: no look-ahead and no back-references.
          </p>
        )}
      </form>

      {otherError && (
        <p role="alert" className="text-destructive text-sm">
          {otherError}
        </p>
      )}

      {isRunning && (
        <p role="status" className="text-muted-foreground text-sm">
          Searching… {matches.length} {matches.length === 1 ? "match" : "matches"} so far.
        </p>
      )}
      {state.phase === "done" && <SearchSummary stats={state.stats} limit={ran.limit} lastMB={ran.lastMB} />}
      {state.phase === "failed" && matches.length > 0 && (
        <p className="text-muted-foreground text-xs">
          The matches below were found before the search stopped; the list is not complete.
        </p>
      )}

      {matches.length > 0 && (
        <div className="flex flex-col gap-2">
          <div className="border-border overflow-x-auto rounded-md border">
            <table className="w-full text-sm">
              <caption className="sr-only">Search matches</caption>
              <thead className="bg-muted text-muted-foreground text-left text-xs">
                <tr>
                  <th scope="col" className="px-3 py-1.5 font-medium">File and line</th>
                  <th scope="col" className="px-3 py-1.5 font-medium">Text</th>
                </tr>
              </thead>
              <tbody className="divide-border divide-y">
                {shown.map((m, i) => (
                  <tr key={`${m.file}:${m.line}:${i}`}>
                    <td className="px-3 py-1.5 align-top font-mono text-xs whitespace-nowrap">
                      {m.file}:{m.line}
                      {m.partial && (
                        <Badge
                          variant="outline"
                          className="ml-2"
                          title="This file was read from the middle, so the line number counts from the first line read."
                        >
                          partial
                          <span className="sr-only">: this file was read from the middle, so the line number counts from the first line read</span>
                        </Badge>
                      )}
                    </td>
                    <td className="px-3 py-1.5 font-mono text-xs break-all whitespace-pre-wrap">{m.text}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {matches.length > RESULT_PAGE_SIZE && (
            <nav aria-label="Search result pages" className="flex flex-wrap items-center justify-between gap-2">
              <p className="text-muted-foreground text-xs" aria-live="polite">
                Showing {firstRow + 1}–{firstRow + shown.length} of {matches.length} matches.
              </p>
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={currentPage === 0}
                  onClick={() => setResultPage(currentPage - 1)}
                >
                  Previous
                </Button>
                <span className="text-xs tabular-nums">
                  Page {currentPage + 1} of {pageCount}
                </span>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={currentPage >= pageCount - 1}
                  onClick={() => setResultPage(currentPage + 1)}
                >
                  Next
                </Button>
              </div>
            </nav>
          )}
        </div>
      )}

      {state.phase === "done" && state.stats.matches === 0 && (
        <p className="text-muted-foreground text-sm">Nothing matched.</p>
      )}
    </div>
  )
}
