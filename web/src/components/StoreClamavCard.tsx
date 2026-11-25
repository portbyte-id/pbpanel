import { Link } from "@tanstack/react-router"
import { CloudDownload, Loader, Trash2 } from "lucide-react"
import { useState } from "react"

import { ErrorState } from "@/components/ErrorState"
import { JobsDrawer } from "@/components/JobsDrawer"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { SkeletonRows } from "@/components/ui/skeleton"
import { ApiError } from "@/lib/api"
import { clamavState, type ClamavAvailability } from "@/lib/clamav"
import { useClamav, useInstallClamav, useUninstallClamav } from "@/lib/clamavQueries"

/**
 * The Store's ClamAV card: the open-source antivirus scanner, installed from the
 * distribution's packages with its signature updater and daemon.
 *
 * Unlike the open-appsec engine there is nothing to acknowledge: ClamAV holds no
 * connection to a vendor beyond the signature mirror freshclam downloads from.
 * What it adds to the panel is the Scan action on the Files page, which runs a
 * scan as a job.
 */
export function StoreClamavCard() {
  const query = useClamav()
  if (query.isPending) return <SkeletonRows label="Loading ClamAV…" />
  if (query.error) {
    if (query.error instanceof ApiError && query.error.status === 503) {
      return (
        <Card>
          <CardHeader>
            <CardTitle>ClamAV</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-muted-foreground text-sm">ClamAV is not available on this host; see the daemon log.</p>
          </CardContent>
        </Card>
      )
    }
    return (
      <ErrorState
        error={query.error}
        fallback="ClamAV's state could not be read."
        onRetry={() => void query.refetch()}
      />
    )
  }
  return <ClamavCardBody a={query.data} />
}

function ClamavCardBody({ a }: { a: ClamavAvailability }) {
  const install = useInstallClamav()
  const uninstall = useUninstallClamav()
  const [jobId, setJobId] = useState<string | null>(null)

  const state = clamavState(a)
  const busy = install.isPending || uninstall.isPending

  const start = (mutation: typeof install | typeof uninstall) => {
    mutation.mutate(undefined, {
      onSuccess: (res) => {
        if (res.job) setJobId(res.job.id)
      },
    })
  }

  return (
    <Card role="region" aria-label="ClamAV">
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle>ClamAV</CardTitle>
        <Badge variant={state.tone}>{state.badge}</Badge>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <p className="text-muted-foreground text-sm">
          An open-source antivirus scanner. Once installed, right-click any file or folder on the{" "}
          <Link to="/files" className="underline underline-offset-4">
            Files page
          </Link>{" "}
          and choose Scan with ClamAV; the result opens as a job.
        </p>

        <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-3">
          <div className="flex flex-col">
            <dt className="text-muted-foreground text-xs">Scanner</dt>
            <dd>{a.installed ? a.version || "installed" : "not installed"}</dd>
          </div>
          <div className="flex flex-col">
            <dt className="text-muted-foreground text-xs">Signatures</dt>
            <dd className="tabular-nums">
              {a.signature_version ? `${a.signature_version} · ${a.signature_date}` : "—"}
            </dd>
          </div>
          <div className="flex flex-col">
            <dt className="text-muted-foreground text-xs">Daemon</dt>
            <dd>{a.installed ? (a.daemon_active ? "running" : "stopped") : "—"}</dd>
          </div>
          <div className="flex flex-col">
            <dt className="text-muted-foreground text-xs">Signature updater</dt>
            <dd>{a.installed ? (a.updater_active ? "running" : "stopped") : "—"}</dd>
          </div>
          <div className="flex flex-col">
            <dt className="text-muted-foreground text-xs">Scan</dt>
            <dd>{a.scanning ? "running now" : "idle"}</dd>
          </div>
        </dl>

        <p className="text-sm" role="status">
          {state.text}
        </p>

        {a.installed ? (
          <div className="flex flex-wrap items-center gap-3">
            <Button
              type="button"
              variant="outline"
              disabled={busy || a.scanning}
              title={a.scanning ? "A scan is running; wait for it to finish." : undefined}
              onClick={() => start(uninstall)}
            >
              {uninstall.isPending ? (
                <>
                  <Loader className="animate-spin" aria-hidden="true" />
                  Starting…
                </>
              ) : (
                <>
                  <Trash2 aria-hidden="true" />
                  Uninstall ClamAV
                </>
              )}
            </Button>
            <p className="text-muted-foreground text-xs">
              Uninstalling removes the packages and the downloaded signatures. Signatures are kept current by the
              updater service, not by this card.
            </p>
          </div>
        ) : (
          <div className="flex flex-col gap-2">
            <div className="flex flex-wrap items-center gap-3">
              <Button type="button" disabled={busy} onClick={() => start(install)}>
                {install.isPending ? (
                  <>
                    <Loader className="animate-spin" aria-hidden="true" />
                    Starting…
                  </>
                ) : (
                  <>
                    <CloudDownload aria-hidden="true" />
                    Install ClamAV
                  </>
                )}
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              Installs {a.packages.join(", ")} with apt, downloads the first virus signatures (around 300 MB), then
              starts the daemon and the updater. The daemon keeps the signatures in memory, about 1 GB.
            </p>
          </div>
        )}

        {install.error && (
          <p role="alert" className="text-destructive text-sm">
            {install.error instanceof ApiError ? install.error.detail : "The install could not be started."}
          </p>
        )}
        {uninstall.error && (
          <p role="alert" className="text-destructive text-sm">
            {uninstall.error instanceof ApiError ? uninstall.error.detail : "The uninstall could not be started."}
          </p>
        )}
        {(install.data?.job || uninstall.data?.job) && (
          <p className="text-muted-foreground text-xs" role="status">
            The job is running. Its output is in Jobs.
          </p>
        )}
      </CardContent>

      <JobsDrawer jobId={jobId} onClose={() => setJobId(null)} />
    </Card>
  )
}
