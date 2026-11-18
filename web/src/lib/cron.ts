/** Cron jobs and their runs, as `GET /api/cron` returns them (docs/design/m7-api.md). */

/** `builtin` is a job the panel runs itself. The API never creates one from a request. */
export type CronKind = "argv" | "script" | "shell" | "builtin"
export type CronOverlap = "skip" | "queue" | "allow"
export type RunStatus =
  | "running"
  | "succeeded"
  | "failed"
  | "timeout"
  | "canceled"
  | "interrupted"
  | "skipped"

/** The run summary on a job row. It carries no output; fetch the run for that. */
export interface RunSummary {
  id: number
  source: "schedule" | "manual"
  status: RunStatus
  exit_code: number
  started_at: string
  finished_at?: string
}

export interface CronJob {
  id: number
  name: string
  schedule: string
  kind: CronKind
  /** The built-in a `builtin` job runs, such as `tls.renew`; empty otherwise. */
  builtin?: string
  user: string
  argv?: string[]
  script_path?: string
  script_body?: string
  workdir?: string
  timeout_seconds: number
  overlap: CronOverlap
  retention: number
  enabled: boolean
  created_at: string
  updated_at: string
  /** Null for a disabled job. */
  next_run: string | null
  /** Null for a job that has never run. */
  last_run: RunSummary | null
}

export interface CronRun {
  id: number
  job_id: number
  source: "schedule" | "manual"
  started_at: string
  finished_at?: string
  exit_code: number
  status: RunStatus
  log_path?: string
  /** The last 8 KiB of the combined output. */
  output_tail: string
  truncated: boolean
  error?: string
}

/** The body of POST and PUT. Zero values mean the service default. */
export interface CronJobInput {
  name?: string
  schedule: string
  kind: CronKind
  user: string
  argv?: string[]
  script_path?: string
  script_body?: string
  workdir?: string
  timeout_seconds?: number
  overlap?: CronOverlap | ""
  retention?: number
  enabled?: boolean
}

export type RunOutcome = "started" | "queued" | "skipped"

export interface RunResponse {
  outcome: RunOutcome
  /** Null for `queued`: a queued firing has no run until the running one ends. */
  run: CronRun | null
}

export const KIND_LABELS: Record<CronKind, string> = {
  argv: "Program with arguments",
  script: "Script file",
  shell: "Shell script",
  builtin: "Built-in",
}

/** What each built-in does, by its id. */
export const BUILTIN_LABELS: Record<string, string> = {
  "tls.renew": "Renews SSL certificates.",
}

/** What a built-in job still lets the operator do. */
export const BUILTIN_NOTE = "You can change when it runs or turn it off, but it cannot be deleted."

export function isBuiltin(job: Pick<CronJob, "kind">): boolean {
  return job.kind === "builtin"
}

export const OVERLAP_LABELS: Record<CronOverlap, string> = {
  skip: "Skip the new run",
  queue: "Run it after the current one",
  allow: "Run both at once",
}

export const STATUS_LABELS: Record<RunStatus, string> = {
  running: "Running",
  succeeded: "Succeeded",
  failed: "Failed",
  timeout: "Timed out",
  canceled: "Canceled",
  interrupted: "Interrupted",
  skipped: "Skipped",
}

/** A run that should draw the eye. `skipped` and `canceled` were the operator's or the policy's doing. */
export function isBadStatus(status: RunStatus): boolean {
  return status === "failed" || status === "timeout" || status === "interrupted"
}

export type RunVariant = "default" | "secondary" | "destructive" | "outline"

export function statusVariant(status: RunStatus): RunVariant {
  if (isBadStatus(status)) return "destructive"
  if (status === "succeeded") return "default"
  if (status === "running") return "secondary"
  return "outline"
}

/** What a "Run now" did, in a sentence for the operator. */
export function outcomeMessage(res: RunResponse, jobName: string): string {
  switch (res.outcome) {
    case "started":
      return `${jobName} started. Its output appears in the run history when it ends.`
    case "queued":
      return `${jobName} is already running, so this firing is queued and starts when that run ends.`
    case "skipped":
      return `${jobName} is already running and its overlap policy skips the new run. A skipped run was recorded.`
  }
}

/** The command a job runs, one line, for the table. */
export function commandSummary(job: Pick<CronJob, "kind" | "argv" | "script_path" | "builtin">): string {
  if (job.kind === "argv") return (job.argv ?? []).join(" ")
  if (job.kind === "script") return job.script_path ?? ""
  if (job.kind === "builtin") return BUILTIN_LABELS[job.builtin ?? ""] ?? "Built-in job"
  return "Shell script"
}

const when = new Intl.DateTimeFormat(undefined, {
  year: "numeric",
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
})

/** A timestamp in the browser's locale, or a dash. */
export function formatWhen(iso: string | null | undefined): string {
  if (!iso) return "–"
  const t = Date.parse(iso)
  return Number.isNaN(t) ? "–" : when.format(new Date(t))
}

/** How long a run took, from its two timestamps; empty while it is still running. */
export function runDuration(run: Pick<CronRun, "started_at" | "finished_at">): string {
  if (!run.finished_at) return ""
  const ms = Date.parse(run.finished_at) - Date.parse(run.started_at)
  if (!Number.isFinite(ms) || ms < 0) return ""
  if (ms < 1000) return "under 1s"
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return m < 60 ? `${m}m ${s % 60}s` : `${Math.floor(m / 60)}h ${m % 60}m`
}
