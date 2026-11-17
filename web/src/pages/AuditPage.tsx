import { SkeletonRows } from "@/components/ui/skeleton"
import { useState } from "react"

import { JobsDrawer } from "@/components/JobsDrawer"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { ApiError, type AuditEntry } from "@/lib/api"
import { useAudit } from "@/lib/queries"

const PAGE_SIZE = 100

/**
 * The audit trail: newest first, straight from `GET /api/audit`.
 *
 * The action column is the HTTP method and path the middleware recorded, so a
 * reader can tell a failed login attempt from a rejected settings write without
 * opening the detail. The detail itself is shown as compact JSON because it is
 * free-form by design and the writer has already redacted it.
 */
export function AuditPage() {
  const { data, isPending, error } = useAudit(PAGE_SIZE)
  const [jobId, setJobId] = useState<string | null>(null)

  if (isPending) {
    return <SkeletonRows label="Loading audit log…" />
  }
  if (error) {
    return (
      <p role="alert" className="text-destructive text-sm">
        {error instanceof ApiError ? error.detail : "The audit log could not be loaded."}
      </p>
    )
  }

  const entries = data?.entries ?? []

  if (entries.length === 0) {
    return (
      <p className="text-muted-foreground text-sm">
        No audited actions yet. Changing a setting, signing in or out, and a rejected request all
        appear here.
      </p>
    )
  }

  return (
    <div className="flex flex-col gap-3">
      <p className="text-muted-foreground text-xs">
        Showing the newest {entries.length} of at most {data?.limit ?? PAGE_SIZE} entries.
      </p>

      <div className="overflow-x-auto rounded-lg border">
        <table className="w-full text-sm">
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
