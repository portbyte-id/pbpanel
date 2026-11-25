import { Link } from "@tanstack/react-router"
import { CloudDownload, Loader, Trash2 } from "lucide-react"
import { useId, useState } from "react"

import { AppsecWarning } from "@/components/AppsecWarning"
import { ErrorState } from "@/components/ErrorState"
import { JobsDrawer } from "@/components/JobsDrawer"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { SkeletonRows } from "@/components/ui/skeleton"
import { ApiError } from "@/lib/api"
import { agentState, canInstall, type AppsecAvailability } from "@/lib/appsec"
import { useAppsec, useInstallAppsec, useUninstallAppsec } from "@/lib/wafQueries"

/**
 * The Store's open-appsec card: Check Point's machine-learning engine, the
 * second one a site's firewall can run.
 *
 * Two things about it are not ordinary. The tracking statement is the API's own
 * `warning` string, rendered verbatim (see `AppsecWarning`) — it is not written
 * here, so it cannot drift from the sentence the operator agreed to. And an
 * install cannot start until that text is acknowledged: the checkbox below is
 * the only thing that unlocks the button, and what it unlocks is a request that
 * sends the server's own `acknowledgement` value back. The API refuses an
 * install without it, so the gate is enforced on both sides.
 */
export function StoreAppsecCard() {
  const query = useAppsec()
  if (query.isPending) return <SkeletonRows label="Loading the open-appsec engine…" />
  if (query.error) {
    // A 503 is the panel saying this host has no appsec package at all, which is
    // a fact about the build rather than a failure to retry.
    const missing = query.error instanceof ApiError && query.error.status === 503
    if (missing) {
      return (
        <Card>
          <CardHeader>
            <CardTitle>open-appsec</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-muted-foreground text-sm">
              This panel build has no open-appsec package, so the second firewall engine cannot be installed from here.
            </p>
          </CardContent>
        </Card>
      )
    }
    return (
      <ErrorState
        error={query.error}
        fallback="The open-appsec engine's state could not be read."
        onRetry={() => void query.refetch()}
      />
    )
  }
  return <AppsecCardBody a={query.data} />
}

function AppsecCardBody({ a }: { a: AppsecAvailability }) {
  const id = useId()
  const install = useInstallAppsec()
  const uninstall = useUninstallAppsec()
  const [acknowledged, setAcknowledged] = useState(false)
  const [jobId, setJobId] = useState<string | null>(null)

  const state = agentState(a)
  const installable = canInstall(a)
  const busy = install.isPending || uninstall.isPending

  const startInstall = () => {
    if (!acknowledged) return
    install.mutate(a.acknowledgement, {
      onSuccess: (res) => {
        setAcknowledged(false)
        if (res.job) setJobId(res.job.id)
      },
    })
  }
  const startUninstall = () => {
    uninstall.mutate(undefined, {
      onSuccess: (res) => {
        if (res.job) setJobId(res.job.id)
      },
    })
  }

  return (
    <Card role="region" aria-label="open-appsec engine">
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle>open-appsec</CardTitle>
        <Badge variant={state.tone}>{state.badge}</Badge>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <p className="text-muted-foreground text-sm">
          Check Point&apos;s machine-learning inspection engine, the second engine a site&apos;s firewall can run. A
          site switched to it on its Firewall tab inspects with this agent instead of ModSecurity.
        </p>

        <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-3">
          <div className="flex flex-col">
            <dt className="text-muted-foreground text-xs">Agent</dt>
            <dd>{a.installed ? a.agent_version || "installed" : "not installed"}</dd>
          </div>
          <div className="flex flex-col">
            <dt className="text-muted-foreground text-xs">Unit</dt>
            <dd>{a.installed ? (a.agent_active ? "running" : "stopped") : "—"}</dd>
          </div>
          <div className="flex flex-col">
            <dt className="text-muted-foreground text-xs">Policy</dt>
            <dd>{a.policy_load || "—"}</dd>
          </div>
          <div className="flex flex-col">
            <dt className="text-muted-foreground text-xs">Pinned release</dt>
            <dd className="tabular-nums">{a.pinned_version || "—"}</dd>
          </div>
          <div className="flex flex-col">
            <dt className="text-muted-foreground text-xs">Sites using it</dt>
            <dd>
              <Link to="/waf" className="underline underline-offset-4">
                see the WAF page
              </Link>
            </dd>
          </div>
        </dl>

        <p className="text-sm" role="status">
          {state.text}
        </p>

        {/* The warning is always on the card, installed or not: it is the reason
            installing is a decision rather than a click. */}
        <AppsecWarning warning={a.warning} />

        {installable ? (
          <div className="flex flex-col gap-3">
            <label className="flex items-start gap-2 text-sm">
              <input
                id={`${id}-ack`}
                type="checkbox"
                className="mt-0.5"
                checked={acknowledged}
                disabled={busy}
                onChange={(e) => setAcknowledged(e.target.checked)}
              />
              <span>
                I have read the statement above. I understand that installing this engine keeps a connection to Check
                Point&apos;s cloud and can subject this host and its traffic to tracking.
              </span>
            </label>
            <div className="flex flex-wrap items-center gap-3">
              <Button type="button" disabled={!acknowledged || busy} onClick={startInstall}>
                {install.isPending ? (
                  <>
                    <Loader className="animate-spin" aria-hidden="true" />
                    Starting…
                  </>
                ) : (
                  <>
                    <CloudDownload aria-hidden="true" />
                    Install open-appsec {a.pinned_version || ""}
                  </>
                )}
              </Button>
              {!acknowledged && (
                <span className="text-muted-foreground text-xs">Tick the box to enable installing.</span>
              )}
            </div>
            <p className="text-muted-foreground text-xs">
              The agent is downloaded from the vendor, its archive checked against the pinned SHA-256, then installed
              in local-policy mode. Installing it also writes an all-inactive policy, so no site is inspected until one
              is switched to the engine.
            </p>
          </div>
        ) : (
          <div className="flex flex-wrap items-center gap-3">
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              title="A site still running the engine makes the uninstall fail with the sites named."
              onClick={startUninstall}
            >
              {uninstall.isPending ? (
                <>
                  <Loader className="animate-spin" aria-hidden="true" />
                  Starting…
                </>
              ) : (
                <>
                  <Trash2 aria-hidden="true" />
                  Uninstall the agent
                </>
              )}
            </Button>
            <p className="text-muted-foreground text-xs">
              Uninstall is refused while any site&apos;s firewall runs open-appsec; switch those sites to ModSecurity
              or off first. The engine is upgraded by a panel release, not from this card.
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
