import { useEffect, useState } from "react"

import { ErrorState } from "@/components/ErrorState"
import { JobsDrawer } from "@/components/JobsDrawer"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { SkeletonRows } from "@/components/ui/skeleton"
import { formatWhen } from "@/lib/cron"
import {
  formatDuration,
  isActiveJob,
  JOB_STATE_LABELS,
  jobElapsedMs,
  type JobRecord,
  type JobState,
} from "@/lib/jobs"
import { useJobs } from "@/lib/queries"

type Scope = "running" | "recent"

/**
 * The panel's background jobs. "Running" lists the live ones; "Recent" adds the
 * finished ones. Watch opens the job's output, which streams as it arrives.
 */
export function JobsPage() {
  const [scope, setScope] = useState<Scope>("running")
  const [watching, setWatching] = useState<string | null>(null)
  const now = useNow()
  const jobs = useJobs({ active: scope === "running" })

  return (
    <div className="flex max-w-6xl flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <p className="text-muted-foreground max-w-2xl text-sm">
          Background work the panel runs, such as installs, builds, file operations and renewals.
          Watch a job to read its output as it arrives. A long job keeps only its newest lines.
        </p>
        <div role="group" aria-label="Which jobs to list" className="flex gap-1">
          <Button
            size="sm"
            variant={scope === "running" ? "default" : "outline"}
            aria-pressed={scope === "running"}
            onClick={() => setScope("running")}
          >
            Running
          </Button>
          <Button
            size="sm"
            variant={scope === "recent" ? "default" : "outline"}
            aria-pressed={scope === "recent"}
            onClick={() => setScope("recent")}
          >
            Recent
          </Button>
        </div>
      </div>

      {jobs.isPending ? (
        <SkeletonRows label="Loading jobs…" />
      ) : jobs.error ? (
        <ErrorState
          error={jobs.error}
          fallback="The jobs could not be loaded."
          onRetry={() => void jobs.refetch()}
        />
      ) : jobs.data.length === 0 ? (
        <EmptyList scope={scope} />
      ) : (
        <JobTable jobs={jobs.data} now={now} onWatch={setWatching} />
      )}

      <JobsDrawer jobId={watching} onClose={() => setWatching(null)} />
    </div>
  )
}

function EmptyList({ scope }: { scope: Scope }) {
  return (
    <div className="border-border flex flex-col gap-2 rounded-md border border-dashed p-6">
      <p className="text-sm font-medium">{scope === "running" ? "Nothing is running" : "No jobs yet"}</p>
      <p className="text-muted-foreground text-sm">
        {scope === "running"
          ? "Jobs appear here while the panel installs, builds or changes something, and leave this list when they finish. Recent lists the finished ones too."
          : "Jobs appear here once the panel starts one, such as a PHP build or a file upload."}
      </p>
    </div>
  )
}

function JobTable({
  jobs,
  now,
  onWatch,
}: {
  jobs: JobRecord[]
  now: number
  onWatch: (id: string) => void
}) {
  return (
    <div className="border-border overflow-x-auto rounded-md border">
      <table className="w-full text-sm">
        <caption className="sr-only">Background jobs</caption>
        <thead className="bg-muted text-muted-foreground text-left text-xs">
          <tr>
            <th scope="col" className="px-3 py-1.5 font-medium">Job</th>
            <th scope="col" className="px-3 py-1.5 font-medium">State</th>
            <th scope="col" className="px-3 py-1.5 font-medium">Started</th>
            <th scope="col" className="px-3 py-1.5 font-medium">Elapsed</th>
            <th scope="col" className="px-3 py-1.5 font-medium">Output</th>
            <th scope="col" className="px-3 py-1.5 font-medium">
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody className="divide-border divide-y">
          {jobs.map((job) => {
            const elapsed = jobElapsedMs(job, now)
            const shortId = job.id.slice(0, 8)
            const live = isActiveJob(job)
            return (
              <tr key={job.id} data-testid="job-row" data-state={job.state} data-job={job.kind}>
                <td className="px-3 py-1.5">
                  <p className="font-mono text-xs font-medium">{job.kind}</p>
                  <p className="text-muted-foreground font-mono text-xs" title={job.id}>
                    {shortId}
                  </p>
                </td>
                <td className="px-3 py-1.5">
                  <StateBadge state={job.state} />
                  {job.error && (
                    <p className="text-destructive mt-1 max-w-xs truncate text-xs" title={job.error}>
                      {job.error}
                    </p>
                  )}
                </td>
                <td className="px-3 py-1.5 text-xs whitespace-nowrap">
                  {formatWhen(job.started_at ?? job.created_at)}
                </td>
                <td className="px-3 py-1.5 font-mono text-xs whitespace-nowrap">
                  {elapsed === null ? "–" : formatDuration(elapsed)}
                </td>
                <td className="px-3 py-1.5 text-xs whitespace-nowrap">
                  {job.last_seq === 0 ? "No output" : `${job.last_seq} lines`}
                  {job.truncated && (
                    <span className="text-muted-foreground block">older lines dropped</span>
                  )}
                </td>
                <td className="px-3 py-1.5 text-right">
                  <Button
                    size="sm"
                    variant="outline"
                    aria-label={`${live ? "Watch" : "Show output of"} ${job.kind} job ${shortId}`}
                    onClick={() => onWatch(job.id)}
                  >
                    {live ? "Watch" : "Output"}
                  </Button>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function StateBadge({ state }: { state: JobState }) {
  return <Badge variant={stateVariant(state)}>{JOB_STATE_LABELS[state]}</Badge>
}

function stateVariant(state: JobState): "default" | "secondary" | "destructive" | "outline" {
  switch (state) {
    case "running":
    case "queued":
      return "secondary"
    case "succeeded":
      return "default"
    case "failed":
    case "interrupted":
      return "destructive"
    default:
      return "outline"
  }
}

/** The current time, refreshed each second so a live job's elapsed time keeps moving. */
function useNow(): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(id)
  }, [])
  return now
}
