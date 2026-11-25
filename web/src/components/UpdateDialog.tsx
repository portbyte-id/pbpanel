import { useEffect, useState } from "react"

import { ReleaseNotes } from "@/components/ReleaseNotes"
import { Button } from "@/components/ui/button"
import { Callout } from "@/components/ui/callout"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Progress } from "@/components/ui/progress"
import { ApiError, type UpgradeStatus } from "@/lib/api"
import { formatDate } from "@/lib/tls"
import { useCheckForUpdates, useStartUpgrade, useUpdateInfo, useUpgradeStatus } from "@/lib/queries"
import {
  formatBytes,
  isUpgradeActive,
  UPGRADE_STEPS,
  upgradePercent,
  upgradePhaseLabel,
  upgradeStepIndex,
} from "@/lib/update"

/**
 * The panel's version, what the newest release changed, and the upgrade. The
 * footer opens it; it reads the same queries the footer does, so the two agree.
 */
export function UpdateDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const info = useUpdateInfo()
  const status = useUpgradeStatus()
  const check = useCheckForUpdates()
  const start = useStartUpgrade()
  const [confirming, setConfirming] = useState(false)
  // True once this page has watched an upgrade run. A "done" record left by an
  // earlier upgrade must not reload the page, or it would reload forever.
  const [sawActive, setSawActive] = useState(false)

  const release = info.data
  const latest = release?.latest ?? ""
  const upgrade = status.data
  const active = isUpgradeActive(upgrade?.phase)
  const finished = upgrade?.phase === "done"
  const reloading = finished && sawActive

  useEffect(() => {
    if (active) setSawActive(true)
  }, [active])

  // The panel now serves the new web bundle; this tab still has the old one.
  useEffect(() => {
    if (!reloading) return
    const timer = window.setTimeout(() => window.location.reload(), 3000)
    return () => window.clearTimeout(timer)
  }, [reloading])
  const showProgress = upgrade !== undefined && upgrade.phase !== "idle"

  const startUpgrade = () => {
    if (!latest) return
    start.mutate(latest, { onSettled: () => setConfirming(false) })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[640px]">
        <DialogHeader>
          <DialogTitle>Panel version</DialogTitle>
          <DialogDescription>
            {release ? `Running ${release.current}` : info.isError ? "The version could not be read." : "Checking the version…"}
          </DialogDescription>
        </DialogHeader>

        {release?.error && (
          <Callout tone="warning" className="text-sm">
            Could not check GitHub for a newer release: {release.error}
          </Callout>
        )}

        {showProgress && upgrade && (
          <UpgradeProgress status={upgrade} reconnecting={status.isError && active} />
        )}

        {reloading && (
          <p className="text-muted-foreground text-sm">Upgrade complete. Reloading to load the new version…</p>
        )}

        {release?.available && (
          <section aria-label="What changed" className="space-y-3">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <h3 className="font-semibold">What changed in {release.latest}</h3>
              <p className="text-muted-foreground text-xs">
                {release.published_at && `Published ${formatDate(release.published_at)}`}
                {release.release_url && (
                  <>
                    {" · "}
                    <a
                      href={release.release_url}
                      target="_blank"
                      rel="noreferrer"
                      className="text-primary underline-offset-4 hover:underline"
                    >
                      Release page
                    </a>
                  </>
                )}
              </p>
            </div>
            <div className="max-h-[40vh] overflow-y-auto pr-1">
              <ReleaseNotes markdown={release.changelog} />
            </div>
          </section>
        )}

        {release && !release.available && !release.error && (
          <p className="text-muted-foreground text-sm">You are running the latest version.</p>
        )}

        {start.isError && (
          <Callout tone="destructive" className="text-sm">
            {start.error instanceof ApiError ? start.error.detail : "The upgrade could not be started."}
          </Callout>
        )}

        <DialogFooter className="flex-wrap gap-2 sm:justify-between">
          <Button
            type="button"
            variant="outline"
            onClick={() => check.mutate()}
            disabled={check.isPending || active}
          >
            {check.isPending ? "Checking…" : "Check again"}
          </Button>

          {release?.available && !active && !finished && !confirming && (
            <div className="flex flex-col items-end gap-1">
              <Button type="button" onClick={() => setConfirming(true)} disabled={!release.installable}>
                Upgrade to {release.latest}
              </Button>
              {!release.installable && (
                <p className="text-muted-foreground text-xs">This release has no binary for this host yet.</p>
              )}
            </div>
          )}

          {release?.available && !active && !finished && confirming && (
            <div className="flex flex-col items-end gap-2 sm:flex-row sm:items-center">
              <p className="text-muted-foreground max-w-[30ch] text-right text-xs">
                The panel restarts at the end of the upgrade. This page reconnects when it is back.
              </p>
              <div className="flex gap-2">
                <Button type="button" variant="outline" onClick={() => setConfirming(false)}>
                  Cancel
                </Button>
                <Button type="button" onClick={startUpgrade} disabled={start.isPending}>
                  {start.isPending ? "Starting…" : `Start upgrade`}
                </Button>
              </div>
            </div>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** The upgrade's stepper, its progress bar and the output it has printed. */
function UpgradeProgress({ status, reconnecting }: { status: UpgradeStatus; reconnecting: boolean }) {
  const failed = status.phase === "failed" || status.phase === "rolled_back" || status.phase === "interrupted"
  const complete = status.phase === "done" || status.phase === "up_to_date"
  const current = upgradeStepIndex(status.phase)
  const percent = upgradePercent(status)
  const barValue = complete ? 100 : failed ? 0 : percent

  return (
    <section aria-label="Upgrade progress" className="space-y-3 rounded-lg border p-4">
      <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
        <span className="font-medium">{upgradePhaseLabel(status.phase)}</span>
        {status.tag && status.tag !== "latest" && (
          <span className="text-muted-foreground font-mono text-xs">{status.tag}</span>
        )}
      </div>

      <Progress value={barValue} label="Upgrade progress" />

      {status.phase === "downloading" && (
        <p className="text-muted-foreground text-xs tabular-nums">
          {status.total > 0
            ? `${formatBytes(status.done)} of ${formatBytes(status.total)}`
            : `${formatBytes(status.done)} downloaded`}
        </p>
      )}

      <ol className="grid gap-1 text-xs sm:grid-cols-6">
        {UPGRADE_STEPS.map((step, i) => {
          const state = complete || i < current ? "done" : i === current && !failed ? "current" : "todo"
          return (
            <li
              key={step.phase}
              aria-current={state === "current" ? "step" : undefined}
              className={
                state === "done"
                  ? "text-foreground"
                  : state === "current"
                    ? "text-primary font-medium"
                    : "text-muted-foreground"
              }
            >
              <span aria-hidden className="mr-1">
                {state === "done" ? "✓" : state === "current" ? "•" : "○"}
              </span>
              {step.label}
            </li>
          )
        })}
      </ol>

      {status.message && !failed && !complete && (
        <p className="text-muted-foreground text-xs">{status.message}</p>
      )}

      {reconnecting && <p className="text-muted-foreground text-xs">Waiting for the panel to answer again…</p>}

      {status.error && (
        <Callout tone="destructive" className="text-sm">
          {status.error}
        </Callout>
      )}

      {status.log.length > 0 && (
        <details className="text-xs">
          <summary className="text-muted-foreground cursor-pointer">Output</summary>
          <pre className="bg-muted mt-2 max-h-48 overflow-auto rounded-md p-3 font-mono leading-snug whitespace-pre-wrap">
            {status.log.slice(-80).join("\n")}
          </pre>
        </details>
      )}
    </section>
  )
}
