import { ErrorState } from "@/components/ErrorState"
import { SkeletonRows } from "@/components/ui/skeleton"
import { useId, useState } from "react"

import { JobsDrawer } from "@/components/JobsDrawer"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Select } from "@/components/ui/select"
import { TableFilter, TableToolbar } from "@/components/ui/table-controls"
import { type AuditEntry } from "@/lib/api"
import { useAudit } from "@/lib/queries"
import { matchesQuery } from "@/lib/table"

const PAGE_SIZE_CHOICES = [25, 50, 100, 200]
const DEFAULT_PAGE_SIZE = 50

/**
 * Where the reader is in the trail: the page size, and the `before` cursor of
 * every page above the current one. The first page has no cursor, so Newer
 * just pops the stack and returns to the exact page it came from.
 */
interface Paging {
  size: number
  cursors: number[]
}

/**
 * The audit trail: newest first, straight from `GET /api/audit`, a page at a
 * time. Paging is by cursor, not offset, so a write that lands while the reader
 * is on an older page does not shift the rows they are reading.
 *
 * The action column is the HTTP method and path the middleware recorded, so a
 * reader can tell a failed login attempt from a rejected settings write without
 * opening the detail. The detail itself is shown as compact JSON because it is
 * free-form by design and the writer has already redacted it.
 */
export function AuditPage() {
  const sizeId = useId()
  const [paging, setPaging] = useState<Paging>({ size: DEFAULT_PAGE_SIZE, cursors: [] })
  const [jobId, setJobId] = useState<string | null>(null)
  const [filter, setFilter] = useState("")
  const [resultFilter, setResultFilter] = useState("all")

  const before = paging.cursors.length > 0 ? paging.cursors[paging.cursors.length - 1] : undefined
  const { data, isPending, isPlaceholderData, error, refetch } = useAudit(paging.size, before)

  if (isPending) {
    return <SkeletonRows label="Loading audit log…" />
  }
  if (error) {
    return <ErrorState error={error} fallback="The audit log could not be loaded." onRetry={() => void refetch()} />
  }

  const allEntries = data?.entries ?? []
  // The server pages the trail by cursor; these filters narrow the page on screen.
  const entries = allEntries.filter(
    (e) =>
      (resultFilter === "all" || e.result === resultFilter) &&
      matchesQuery([e.actor, e.action, e.target, e.result, summarize(e.detail)], filter),
  )
  const resultOptions = [...new Set(allEntries.map((e) => e.result))].map((r) => ({ value: r, label: r }))
  const page = paging.cursors.length + 1
  const hasNewer = paging.cursors.length > 0
  const hasOlder = (data?.next_before ?? 0) > 0

  const olderPage = () => {
    if (!data || !hasOlder) return
    setPaging((p) => ({ ...p, cursors: [...p.cursors, data.next_before] }))
  }
  const newerPage = () => setPaging((p) => ({ ...p, cursors: p.cursors.slice(0, -1) }))

  if (allEntries.length === 0 && !hasNewer) {
    return (
      <p className="text-muted-foreground text-sm">
        No audited actions yet. Changing a setting, signing in or out, and a rejected request all
        appear here.
      </p>
    )
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={sizeId}>Entries per page</Label>
          <Select
            id={sizeId}
            className="w-28"
            value={paging.size}
            onChange={(e) => setPaging({ size: Number(e.target.value), cursors: [] })}
          >
            {PAGE_SIZE_CHOICES.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </Select>
        </div>
        <p className="text-muted-foreground text-xs" aria-live="polite">
          Page {page} · {entries.length} {entries.length === 1 ? "entry" : "entries"}, newest first
        </p>
      </div>

      <TableToolbar search={filter} onSearchChange={setFilter} searchLabel="Filter audit entries" placeholder="Filter this page">
        <TableFilter label="Result" value={resultFilter} onChange={setResultFilter} options={resultOptions} />
      </TableToolbar>

      {entries.length === 0 ? (
        <p className="text-muted-foreground text-sm" role="status">
          No rows match the filters.
        </p>
      ) : (
      <div className={`overflow-x-auto rounded-lg border ${isPlaceholderData ? "opacity-60" : ""}`}>
        <table className="w-full text-sm">
          <caption className="sr-only">Audit trail, newest first</caption>
          <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
            <tr>
              <th scope="col" className="px-3 py-1.5 font-medium">Time</th>
              <th scope="col" className="px-3 py-1.5 font-medium">Actor</th>
              <th scope="col" className="px-3 py-1.5 font-medium">Action</th>
              <th scope="col" className="px-3 py-1.5 font-medium">Target</th>
              <th scope="col" className="px-3 py-1.5 font-medium">Result</th>
              <th scope="col" className="px-3 py-1.5 font-medium">Detail</th>
            </tr>
          </thead>
          <tbody className="divide-border divide-y">
            {entries.map((entry) => (
              <AuditRow key={entry.id} entry={entry} onOpenJob={setJobId} />
            ))}
          </tbody>
        </table>
      </div>
      )}

      <nav aria-label="Audit pages" className="flex items-center justify-end gap-2">
        <Button type="button" variant="outline" size="sm" disabled={!hasNewer} onClick={newerPage}>
          Newer
        </Button>
        <Button type="button" variant="outline" size="sm" disabled={!hasOlder} onClick={olderPage}>
          Older
        </Button>
      </nav>

      <JobsDrawer jobId={jobId} onClose={() => setJobId(null)} />
    </div>
  )
}

function AuditRow({
  entry,
  onOpenJob,
}: {
  entry: AuditEntry
  onOpenJob: (jobId: string) => void
}) {
  const jobId = jobIdOf(entry)

  return (
    <tr className="align-top">
      <td className="text-muted-foreground px-3 py-1.5 whitespace-nowrap">
        <time dateTime={entry.at}>{formatTime(entry.at)}</time>
      </td>
      <td className="px-3 py-1.5 whitespace-nowrap">{entry.actor}</td>
      <td className="px-3 py-1.5 font-mono text-xs whitespace-nowrap">{entry.action}</td>
      <td className="px-3 py-1.5 font-mono text-xs">{entry.target}</td>
      <td className="px-3 py-1.5">
        <Badge variant={entry.result === "success" ? "secondary" : "destructive"}>
          {entry.result}
        </Badge>
      </td>
      <td className="px-3 py-1.5 text-xs">
        <div className="flex flex-col gap-1">
          <code className="text-muted-foreground break-all">{summarize(entry.detail)}</code>
          {jobId && (
            <Button
              variant="link"
              size="sm"
              className="h-auto self-start p-0 text-xs"
              onClick={() => onOpenJob(jobId)}
            >
              Open job {jobId}
            </Button>
          )}
        </div>
      </td>
    </tr>
  )
}

/**
 * A job id in the detail is what makes a row worth clicking: the drawer streams
 * that job's output. Only a string under `job_id` counts, because that is the
 * field the jobs API uses.
 */
function jobIdOf(entry: AuditEntry): string | null {
  if (typeof entry.detail !== "object" || entry.detail === null) return null
  const candidate = (entry.detail as Record<string, unknown>).job_id
  return typeof candidate === "string" && candidate !== "" ? candidate : null
}

function summarize(detail: unknown): string {
  if (detail === null || detail === undefined) return "—"
  if (typeof detail === "string") return detail
  try {
    return JSON.stringify(detail)
  } catch {
    return "—"
  }
}

/** `at` is the server's fixed-width UTC text; show it in the reader's zone. */
function formatTime(at: string): string {
  const date = new Date(at)
  if (Number.isNaN(date.getTime())) return at
  return date.toLocaleString()
}
