import { ErrorState } from "@/components/ErrorState"
import { SkeletonRows } from "@/components/ui/skeleton"
import { Plus } from "lucide-react"
import { useState } from "react"

import { ConfirmDialog } from "@/components/ConfirmDialog"
import { CronJobDialog, ShellNote } from "@/components/CronJobDialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { ApiError } from "@/lib/api"
import {
  commandSummary,
  formatWhen,
  isBuiltin,
  outcomeMessage,
  runDuration,
  statusVariant,
  STATUS_LABELS,
  type CronJob,
  type CronRun,
} from "@/lib/cron"
import {
  useCronJobs,
  useCronRun,
  useCronRuns,
  useDeleteCronJob,
  useRunCronJob,
  useSetCronEnabled,
} from "@/lib/queries"

/** The server's sentence, with the shell explanation for a 403 `shell_not_allowed`. */
function actionError(error: unknown, fallback: string) {
  if (error instanceof ApiError && error.code === "shell_not_allowed") {
    return (
      <>
        <p>{error.detail || "Shell jobs are not allowed."}</p>
        <ShellNote allowed={false} />
      </>
    )
  }
  return <p>{error instanceof ApiError ? error.detail || fallback : fallback}</p>
}

/** Scheduled jobs: the table, and a dialog each for editing, history and deleting. */
export function CronPage() {
  const jobs = useCronJobs()
  const setEnabled = useSetCronEnabled()
  const run = useRunCronJob()
  const remove = useDeleteCronJob()
  const [editing, setEditing] = useState<CronJob | "new" | null>(null)
  const [history, setHistory] = useState<CronJob | null>(null)
  const [deleting, setDeleting] = useState<CronJob | null>(null)
  const [ran, setRan] = useState<string | null>(null)

  if (jobs.isPending) {
    return <SkeletonRows label="Loading cron jobs…" />
  }
  if (jobs.error) {
    return <ErrorState error={jobs.error} fallback="The cron jobs could not be loaded." onRetry={() => void jobs.refetch()} />
  }

  const list = jobs.data
  const busy = setEnabled.isPending || run.isPending
  const actionFailure = setEnabled.error ?? run.error

  const runNow = (job: CronJob) => {
    setRan(null)
    setEnabled.reset()
    run.mutate(job.id, { onSuccess: (res) => setRan(outcomeMessage(res, job.name)) })
  }
  const toggle = (job: CronJob) => {
    setRan(null)
    run.reset()
    setEnabled.mutate({ id: job.id, enabled: !job.enabled })
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-4">
        <p className="text-muted-foreground max-w-2xl text-sm">
          Jobs the panel runs on a schedule, as the user you choose. Each run&apos;s output is kept
          in its history. A run missed while the panel was down is not made up.
        </p>
        <Button onClick={() => setEditing("new")}>
          <Plus aria-hidden /> Add job
        </Button>
      </div>

      {ran && (
        <p role="status" className="text-sm">
          {ran}
        </p>
      )}
      {actionFailure && (
        <div role="alert" className="text-destructive flex flex-col gap-1 text-sm">
          {actionError(actionFailure, "The action failed.")}
        </div>
      )}

      {list.length === 0 ? (
        <div className="border-border flex flex-col gap-2 rounded-md border border-dashed p-6">
          <p className="text-sm font-medium">No cron jobs yet</p>
          <p className="text-muted-foreground text-sm">
            Add a job to run a program or script on a schedule. Its output and exit status are kept
            for every run.
          </p>
        </div>
      ) : (
        <div className="border-border overflow-x-auto rounded-md border">
          <table className="w-full text-sm">
            <caption className="sr-only">Cron jobs</caption>
            <thead className="bg-muted text-muted-foreground text-left text-xs">
              <tr>
                <th scope="col" className="px-3 py-1.5 font-medium">Name</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Schedule</th>
                <th scope="col" className="px-3 py-1.5 font-medium">User</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Next run</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Last run</th>
                <th scope="col" className="px-3 py-1.5 font-medium">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {list.map((job) => (
                <tr key={job.id} data-testid="cron-row" data-job={job.name}>
                  <td className="px-3 py-1.5">
                    <div className="flex items-center gap-2">
                      <p className="font-medium">{job.name}</p>
                      {isBuiltin(job) && <Badge variant="secondary">Built-in</Badge>}
                    </div>
                    <p
                      className={`text-muted-foreground max-w-xs truncate text-xs ${isBuiltin(job) ? "" : "font-mono"}`}
                      title={commandSummary(job)}
                    >
                      {commandSummary(job)}
                    </p>
                  </td>
                  <td className="px-3 py-1.5 font-mono text-xs whitespace-nowrap">{job.schedule}</td>
                  <td className="px-3 py-1.5">{job.user}</td>
                  <td className="px-3 py-1.5 whitespace-nowrap">
                    {job.enabled ? (
                      formatWhen(job.next_run)
                    ) : (
                      <Badge variant="outline">Disabled</Badge>
                    )}
                  </td>
                  <td className="px-3 py-1.5">
                    {job.last_run ? (
                      <div className="flex flex-col gap-1">
                        <Badge variant={statusVariant(job.last_run.status)} className="w-fit">
                          {STATUS_LABELS[job.last_run.status]}
                        </Badge>
                        <span className="text-muted-foreground text-xs whitespace-nowrap">
                          {formatWhen(job.last_run.started_at)}
                        </span>
                      </div>
                    ) : (
                      <span className="text-muted-foreground">Never run</span>
                    )}
                  </td>
                  <td className="px-3 py-1.5">
                    <div className="flex flex-wrap justify-end gap-1.5">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={busy}
                        aria-label={`Run ${job.name} now`}
                        onClick={() => runNow(job)}
                      >
                        Run now
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        aria-label={`History of ${job.name}`}
                        onClick={() => setHistory(job)}
                      >
                        History
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={busy}
                        aria-label={`${job.enabled ? "Disable" : "Enable"} ${job.name}`}
                        onClick={() => toggle(job)}
                      >
                        {job.enabled ? "Disable" : "Enable"}
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        aria-label={`Edit ${job.name}`}
                        onClick={() => setEditing(job)}
                      >
                        Edit
                      </Button>
                      {!isBuiltin(job) && (
                        <Button
                          size="sm"
                          variant="outline"
                          aria-label={`Delete ${job.name}`}
                          onClick={() => setDeleting(job)}
                        >
                          Delete
                        </Button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <CronJobDialog
        // Remounted per target so the form never shows another job's fields.
        key={editing === "new" ? "new" : (editing?.id ?? "none")}
        open={editing !== null}
        onOpenChange={(open) => !open && setEditing(null)}
        job={editing === "new" ? null : editing}
      />

      <HistoryDialog job={history} onClose={() => setHistory(null)} />

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) {
            setDeleting(null)
            remove.reset()
          }
        }}
        title={`Delete ${deleting?.name ?? "job"}?`}
        confirmLabel="Delete job"
        pendingLabel="Deleting…"
        pending={remove.isPending}
        destructive
        error={
          remove.error instanceof ApiError
            ? remove.error.detail
            : remove.error
              ? "The job could not be deleted."
              : null
        }
        onConfirm={() =>
          deleting && remove.mutate(deleting.id, { onSuccess: () => setDeleting(null) })
        }
      >
        <p>A run in progress is canceled.</p>
        <p>Its run history and log files are removed with it. This cannot be undone.</p>
      </ConfirmDialog>
    </div>
  )
}

/* --- History ------------------------------------------------------------ */

function HistoryDialog({ job, onClose }: { job: CronJob | null; onClose: () => void }) {
  return (
    <Dialog open={job !== null} onOpenChange={(open) => !open && onClose()}>
      {/* The header stays put and only the table scrolls, so the title and the
          close button never scroll away and the output panel stays on screen. */}
      <DialogContent className="flex max-h-[90vh] flex-col overflow-hidden">
        <DialogHeader className="shrink-0">
          <DialogTitle>{job ? `${job.name}: run history` : "Run history"}</DialogTitle>
          <DialogDescription>Newest first. Choose a run to read the end of its output.</DialogDescription>
        </DialogHeader>
        {job && <RunHistory job={job} />}
      </DialogContent>
    </Dialog>
  )
}

export function RunHistory({ job }: { job: CronJob }) {
  const runs = useCronRuns(job.id)
  const [openRun, setOpenRun] = useState<number | null>(null)

  if (runs.isPending) return <SkeletonRows label="Loading runs…" />
  if (runs.error) {
    return (
      <p role="alert" className="text-destructive text-sm">
        {runs.error instanceof ApiError ? runs.error.detail : "The run history could not be loaded."}
      </p>
    )
  }
  if (runs.data.length === 0) {
    return <p className="text-muted-foreground text-sm">This job has not run yet.</p>
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      {/* Only the run list scrolls. The output panel keeps its place under it, so
          asking for a run's output never means scrolling past every row first. */}
      <div className="border-border min-h-0 overflow-auto rounded-md border">
        <table className="w-full text-sm">
          <caption className="sr-only">Runs of {job.name}</caption>
          <thead className="bg-muted text-muted-foreground sticky top-0 text-left text-xs">
            <tr>
              <th scope="col" className="px-3 py-1.5 font-medium">Started</th>
              <th scope="col" className="px-3 py-1.5 font-medium">Trigger</th>
              <th scope="col" className="px-3 py-1.5 font-medium">Result</th>
              <th scope="col" className="px-3 py-1.5 font-medium">Exit</th>
              <th scope="col" className="px-3 py-1.5 font-medium">Took</th>
              <th scope="col" className="px-3 py-1.5 font-medium">
                <span className="sr-only">Output</span>
              </th>
            </tr>
          </thead>
          <tbody className="divide-border divide-y">
            {runs.data.map((r) => (
              <tr key={r.id} data-testid="run-row">
                <td className="px-3 py-1.5 whitespace-nowrap">{formatWhen(r.started_at)}</td>
                <td className="px-3 py-1.5">{r.source === "manual" ? "Run now" : "Schedule"}</td>
                <td className="px-3 py-1.5">
                  <Badge variant={statusVariant(r.status)}>{STATUS_LABELS[r.status]}</Badge>
                </td>
                <td className="px-3 py-1.5 tabular-nums">{r.status === "running" ? "–" : r.exit_code}</td>
                <td className="px-3 py-1.5">{runDuration(r) || "–"}</td>
                <td className="px-3 py-1.5 text-right">
                  <Button
                    size="sm"
                    variant="outline"
                    aria-pressed={openRun === r.id}
                    aria-label={`Output of the run started ${formatWhen(r.started_at)}`}
                    onClick={() => setOpenRun(openRun === r.id ? null : r.id)}
                  >
                    Output
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {openRun !== null && (
        <div className="shrink-0">
          <RunOutput
            jobId={job.id}
            row={runs.data.find((r) => r.id === openRun) ?? null}
            runId={openRun}
          />
        </div>
      )}
    </div>
  )
}

function RunOutput({ jobId, runId, row }: { jobId: number; runId: number; row: CronRun | null }) {
  const detail = useCronRun(jobId, runId)
  const run = detail.data ?? row

  if (!run) {
    return detail.error ? (
      <p role="alert" className="text-destructive text-sm">
        {detail.error instanceof ApiError ? detail.error.detail : "The run could not be loaded."}
      </p>
    ) : (
      <SkeletonRows label="Loading output…" />
    )
  }

  return (
    <div className="flex flex-col gap-2">
      {run.error && (
        <p role="alert" className="text-destructive text-sm">
          {run.error}
        </p>
      )}
      <pre
        tabIndex={0}
        aria-label={`Output of run ${run.id}`}
        className="bg-muted max-h-72 overflow-auto rounded-md p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all"
      >
        {run.output_tail !== "" ? run.output_tail : run.status === "running" ? "No output yet." : "No output."}
      </pre>
      {run.truncated && (
        <p className="text-muted-foreground text-xs">
          Only the last 8 KiB is shown.
          {run.log_path ? ` The whole output is in ${run.log_path}.` : ""}
        </p>
      )}
    </div>
  )
}
