import { Link } from "@tanstack/react-router"
import { useId, useState } from "react"

import { parseLines } from "@/components/AppDialog"
import { ApplyError } from "@/components/ApplyError"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select } from "@/components/ui/select"
import { ApiError, type SettingsResponse } from "@/lib/api"
import {
  BUILTIN_LABELS,
  BUILTIN_NOTE,
  isBuiltin,
  KIND_LABELS,
  OVERLAP_LABELS,
  type CronJob,
  type CronJobInput,
  type CronKind,
  type CronOverlap,
} from "@/lib/cron"
import { useSaveCronJob, useSettings } from "@/lib/queries"

/** The setting that gates shell jobs. */
export const ALLOW_SHELL_KEY = "cron.allow_shell"

/** True or false as the Settings registry says; null when it could not be read. */
export function shellAllowed(settings: SettingsResponse | undefined): boolean | null {
  if (!settings) return null
  const key = settings.groups.flatMap((g) => g.keys).find((k) => k.name === ALLOW_SHELL_KEY)
  return key ? key.value === "true" : null
}

const textarea =
  "border-input bg-transparent shadow-xs focus-visible:border-ring focus-visible:ring-ring/50 " +
  "aria-invalid:border-destructive min-h-20 w-full rounded-md border px-3 py-2 font-mono text-sm " +
  "outline-none focus-visible:ring-[3px]"

/** Why a shell job is refused, and where the switch is. Shown beside the option and after a 403. */
export function ShellNote({ allowed }: { allowed: boolean | null }) {
  return (
    <p className="text-muted-foreground text-xs" data-testid="shell-note">
      A shell job stores your script as a file and runs it with <code translate="no">/bin/sh</code>. It is refused
      unless the <code translate="no">{ALLOW_SHELL_KEY}</code> setting is on
      {allowed === false && " and it is off now"}. Turn it on under Cron on the{" "}
      <Link to="/settings" className="underline underline-offset-2">
        Settings page
      </Link>
      .
    </p>
  )
}

const FIELDS = new Set([
  "name",
  "schedule",
  "user",
  "argv",
  "script_path",
  "script_body",
  "workdir",
  "timeout_seconds",
  "overlap",
  "retention",
])

/**
 * The job form, without the dialog or the request around it. A rejected field
 * (`invalid_input` or `conflict` with `field`) is shown under that field; a 403
 * `shell_not_allowed` explains the setting; anything else once under the form.
 */
export function CronJobForm({
  job,
  shellAllowed: shellOn,
  onSubmit,
  onCancel,
  pending,
  error,
}: {
  /** Set when editing: the name is then fixed. */
  job: CronJob | null
  /** The `cron.allow_shell` setting; null when it could not be read. */
  shellAllowed: boolean | null
  onSubmit: (input: CronJobInput) => void
  onCancel: () => void
  pending: boolean
  error: unknown
}) {
  const id = useId()
  const [name, setName] = useState(job?.name ?? "")
  const [schedule, setSchedule] = useState(job?.schedule ?? "")
  const [user, setUser] = useState(job?.user ?? "root")
  const [kind, setKind] = useState<CronKind>(job?.kind ?? "argv")
  const [argv, setArgv] = useState((job?.argv ?? []).join("\n"))
  const [scriptPath, setScriptPath] = useState(job?.script_path ?? "")
  const [scriptBody, setScriptBody] = useState(job?.script_body ?? "")
  const [workdir, setWorkdir] = useState(job?.workdir ?? "")
  const [timeout, setTimeoutText] = useState(job ? String(job.timeout_seconds) : "")
  const [overlap, setOverlap] = useState<CronOverlap>(job?.overlap ?? "skip")
  const [retention, setRetention] = useState(job ? String(job.retention) : "")
  const [enabled, setEnabled] = useState(true)
  const [local, setLocal] = useState<{ field: string; text: string } | null>(null)
  // A built-in keeps its account and command; only its timing and policy are edited.
  const builtinJob = job !== null && isBuiltin(job)

  const apiField = error instanceof ApiError && error.field ? error.field.replace(/\[\d+\]$/, "") : null
  const fieldName =
    error instanceof ApiError &&
    (error.code === "invalid_input" || error.code === "conflict") &&
    apiField !== null &&
    FIELDS.has(apiField)
      ? apiField
      : null
  const shellRefused = error instanceof ApiError && error.code === "shell_not_allowed"

  const fieldError = (n: string) => {
    if (local?.field === n) {
      return (
        <p id={`${id}-${n}-error`} role="alert" className="text-destructive text-xs">
          {local.text}
        </p>
      )
    }
    return fieldName === n && error instanceof ApiError ? (
      <p id={`${id}-${n}-error`} role="alert" className="text-destructive text-xs">
        {error.detail.replace(`${error.field}: `, "")}
      </p>
    ) : null
  }
  const invalid = (n: string) => fieldName === n || local?.field === n
  const describedBy = (n: string) => (invalid(n) ? `${id}-${n}-error` : undefined)
  const generalError = fieldName === null && !shellRefused ? error : null

  const parseCount = (text: string, field: string, label: string): number | undefined | null => {
    if (text.trim() === "") return undefined
    const n = Number(text)
    if (!Number.isInteger(n) || n < 0) {
      setLocal({ field, text: `${label} must be a whole number, or empty for the default.` })
      return null
    }
    return n
  }

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    const timeoutSeconds = parseCount(timeout, "timeout_seconds", "The timeout")
    if (timeoutSeconds === null) return
    const keep = parseCount(retention, "retention", "The number of runs to keep")
    if (keep === null) return
    setLocal(null)

    const input: CronJobInput = {
      schedule: schedule.trim(),
      kind,
      user: user.trim(),
      overlap,
    }
    if (job === null) {
      input.name = name.trim()
      input.enabled = enabled
    }
    if (kind === "argv") input.argv = parseLines(argv)
    if (kind === "script") input.script_path = scriptPath.trim()
    if (kind === "shell") input.script_body = scriptBody
    if (workdir.trim() !== "") input.workdir = workdir.trim()
    if (timeoutSeconds !== undefined) input.timeout_seconds = timeoutSeconds
    if (keep !== undefined) input.retention = keep
    onSubmit(input)
  }

  // An existing shell job stays editable as shell while the setting is off, so the
  // option is only disabled for a job that is not already one.
  const shellDisabled = shellOn === false && job?.kind !== "shell"

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-name`}>Name</Label>
        <Input
          id={`${id}-name`}
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={job !== null}
          autoComplete="off"
          spellCheck={false}
          placeholder="nightly-backup…"
          required
          aria-invalid={invalid("name")}
          aria-describedby={describedBy("name")}
        />
        {fieldError("name")}
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-schedule`}>Schedule</Label>
        <Input
          id={`${id}-schedule`}
          value={schedule}
          onChange={(e) => setSchedule(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          className="font-mono"
          placeholder="0 3 * * *…"
          required
          aria-invalid={invalid("schedule")}
          aria-describedby={describedBy("schedule") ?? `${id}-schedule-hint`}
        />
        {fieldError("schedule")}
        <p id={`${id}-schedule-hint`} className="text-muted-foreground text-xs">
          Five fields: minute, hour, day of month, month, day of week. <code translate="no">0 3 * * *</code> is every
          day at 03:00. A run missed while the panel was down is not made up.
        </p>
      </div>

      {builtinJob ? (
        <p className="text-muted-foreground text-sm" data-testid="builtin-note">
          {`${BUILTIN_LABELS[job?.builtin ?? ""] ?? "Runs inside the panel."} ${BUILTIN_NOTE}`}
        </p>
      ) : (
        <>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-user`}>Runs as</Label>
            <Input
              id={`${id}-user`}
              value={user}
              onChange={(e) => setUser(e.target.value)}
              autoComplete="off"
              spellCheck={false}
              required
              aria-invalid={invalid("user")}
              aria-describedby={describedBy("user")}
            />
            {fieldError("user")}
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-kind`}>What it runs</Label>
            <Select
              id={`${id}-kind`}
              value={kind}
              onChange={(e) => setKind(e.target.value as CronKind)}
              aria-describedby={kind === "shell" || shellDisabled ? `${id}-shell-note` : undefined}
            >
              <option value="argv">{KIND_LABELS.argv}</option>
              <option value="script">{KIND_LABELS.script}</option>
              <option value="shell" disabled={shellDisabled}>
                {KIND_LABELS.shell}
                {shellDisabled ? " (turned off in Settings)" : ""}
              </option>
            </Select>
            {(kind === "shell" || shellDisabled || shellRefused) && (
              <div id={`${id}-shell-note`}>
                {/* A refusal from the server is the better witness than a setting read earlier. */}
                <ShellNote allowed={shellRefused ? false : shellOn} />
              </div>
            )}
          </div>
        </>
      )}

      {kind === "argv" && (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-argv`}>Command and arguments</Label>
          <textarea
            id={`${id}-argv`}
            className={textarea}
            value={argv}
            onChange={(e) => setArgv(e.target.value)}
            spellCheck={false}
            placeholder={"/usr/bin/php\n/www/wwwroot/app/artisan\nschedule:run"}
            aria-invalid={invalid("argv")}
            aria-describedby={describedBy("argv") ?? `${id}-argv-hint`}
          />
          {fieldError("argv")}
          <p id={`${id}-argv-hint`} className="text-muted-foreground text-xs">
            One per line. The first line is the program and must be an absolute path. Nothing is
            passed through a shell, so <code translate="no">*</code>, <code translate="no">|</code> and <code translate="no">&gt;</code> have no
            special meaning.
          </p>
        </div>
      )}

      {kind === "script" && (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-script-path`}>Script path</Label>
          <Input
            id={`${id}-script-path`}
            value={scriptPath}
            onChange={(e) => setScriptPath(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
            placeholder="/www/scripts/backup.sh…"
            aria-invalid={invalid("script_path")}
            aria-describedby={describedBy("script_path")}
          />
          {fieldError("script_path")}
          <p className="text-muted-foreground text-xs">An existing executable file.</p>
        </div>
      )}

      {kind === "shell" && (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-script-body`}>Script</Label>
          <textarea
            id={`${id}-script-body`}
            className={`${textarea} min-h-32`}
            value={scriptBody}
            onChange={(e) => setScriptBody(e.target.value)}
            spellCheck={false}
            placeholder={"set -e\ndate >> /tmp/cron-test.txt"}
            aria-invalid={invalid("script_body")}
            aria-describedby={describedBy("script_body")}
          />
          {fieldError("script_body")}
        </div>
      )}

      {!builtinJob && (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-workdir`}>Working directory</Label>
          <Input
            id={`${id}-workdir`}
            value={workdir}
            onChange={(e) => setWorkdir(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
            placeholder="optional…"
            aria-invalid={invalid("workdir")}
            aria-describedby={describedBy("workdir")}
          />
          {fieldError("workdir")}
        </div>
      )}

      <div className="grid grid-cols-2 gap-4">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-timeout`}>Timeout (seconds)</Label>
          <Input
            id={`${id}-timeout`}
            value={timeout}
            onChange={(e) => setTimeoutText(e.target.value)}
            inputMode="numeric"
            autoComplete="off"
            placeholder="3600…"
            aria-invalid={invalid("timeout_seconds")}
            aria-describedby={describedBy("timeout_seconds")}
          />
          {fieldError("timeout_seconds")}
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-retention`}>Runs to keep</Label>
          <Input
            id={`${id}-retention`}
            value={retention}
            onChange={(e) => setRetention(e.target.value)}
            inputMode="numeric"
            autoComplete="off"
            placeholder="50…"
            aria-invalid={invalid("retention")}
            aria-describedby={describedBy("retention")}
          />
          {fieldError("retention")}
        </div>
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-overlap`}>If the previous run is still going</Label>
        <Select
          id={`${id}-overlap`}
          value={overlap}
          onChange={(e) => setOverlap(e.target.value as CronOverlap)}
          aria-invalid={invalid("overlap")}
        >
          {(Object.keys(OVERLAP_LABELS) as CronOverlap[]).map((o) => (
            <option key={o} value={o}>
              {OVERLAP_LABELS[o]}
            </option>
          ))}
        </Select>
        {fieldError("overlap")}
      </div>

      {job === null && (
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          Start scheduling it now
        </label>
      )}

      <ApplyError error={generalError} fallback="The job could not be saved." />

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={pending}>
          {pending ? "Saving…" : job ? "Save changes" : "Create job"}
        </Button>
      </DialogFooter>
    </form>
  )
}

/** The add and edit dialog. Closing it forgets a failed save. */
export function CronJobDialog({
  open,
  onOpenChange,
  job,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  job: CronJob | null
}) {
  const save = useSaveCronJob()
  const settings = useSettings()

  const close = (next: boolean) => {
    if (!next) save.reset()
    onOpenChange(next)
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{job ? `Edit ${job.name}` : "Add a cron job"}</DialogTitle>
          <DialogDescription>
            {job && isBuiltin(job)
              ? "A built-in job runs inside the panel. Its output is kept in its run history."
              : "The job runs as the user you name, with the output kept in its run history."}
          </DialogDescription>
        </DialogHeader>
        {open && (
          <CronJobForm
            job={job}
            shellAllowed={shellAllowed(settings.data)}
            pending={save.isPending}
            error={save.error}
            onCancel={() => close(false)}
            onSubmit={(input) =>
              save.mutate({ id: job?.id, job: input }, { onSuccess: () => close(false) })
            }
          />
        )}
      </DialogContent>
    </Dialog>
  )
}
