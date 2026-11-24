import { useEffect, useRef, useState } from "react"

import { UpdateDialog } from "@/components/UpdateDialog"
import { useUpdateInfo, useUpgradeStatus } from "@/lib/queries"
import { isUpgradeActive, upgradePhaseLabel } from "@/lib/update"

/**
 * The footer's product name, running version and update state. A newer release
 * shows as a badge; clicking the version or the badge opens the version dialog.
 *
 * When an upgrade this page watched finishes, the page reloads: the new binary
 * serves a new UI bundle, and the old one would be talking to a stale build.
 */
export function VersionControl() {
  const info = useUpdateInfo()
  const status = useUpgradeStatus()
  const [open, setOpen] = useState(false)
  const sawActive = useRef(false)

  const phase = status.data?.phase
  useEffect(() => {
    if (isUpgradeActive(phase)) {
      sawActive.current = true
    }
    if (phase === "done" && sawActive.current) {
      const timer = window.setTimeout(() => window.location.reload(), 1500)
      return () => window.clearTimeout(timer)
    }
  }, [phase])

  const release = info.data
  const upgrading = isUpgradeActive(phase)
  const tag = status.data?.tag
  const badge = upgrading
    ? `Upgrading${tag && tag !== "latest" ? ` to ${tag}` : ""}…`
    : release?.available
      ? `Update ${release.latest}`
      : null

  return (
    <>
      <span>Server Panel</span>
      <span aria-hidden>·</span>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="hover:text-foreground focus-visible:ring-ring rounded font-mono tabular-nums underline-offset-4 hover:underline focus-visible:ring-2 focus-visible:outline-none"
        aria-label={release ? `Panel version ${release.current}` : "Panel version"}
      >
        {release?.current ?? "version"}
      </button>
      {badge && (
        <button
          type="button"
          onClick={() => setOpen(true)}
          title={upgrading && phase ? upgradePhaseLabel(phase) : "A newer version is available"}
          className="bg-primary/10 text-primary hover:bg-primary/15 focus-visible:ring-ring rounded-full px-2 py-0.5 font-medium focus-visible:ring-2 focus-visible:outline-none"
        >
          {badge}
        </button>
      )}
      <UpdateDialog open={open} onOpenChange={setOpen} />
    </>
  )
}
