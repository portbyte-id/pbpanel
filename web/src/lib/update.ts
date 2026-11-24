import type { UpgradePhase, UpgradeStatus } from "@/lib/api"

/** Phases in the order an upgrade goes through them, for the stepper. */
export const UPGRADE_STEPS: { phase: UpgradePhase; label: string }[] = [
  { phase: "checking", label: "Check release" },
  { phase: "downloading", label: "Download" },
  { phase: "installing", label: "Install" },
  { phase: "restarting", label: "Restart panel" },
  { phase: "health", label: "Health check" },
  { phase: "done", label: "Done" },
]

/** Phases that have not finished yet: the status is polled while one is current. */
const ACTIVE: ReadonlySet<UpgradePhase> = new Set([
  "queued",
  "checking",
  "downloading",
  "installing",
  "restarting",
  "health",
])

export function isUpgradeActive(phase: UpgradePhase | undefined): boolean {
  return phase !== undefined && ACTIVE.has(phase)
}

/** The stepper index of a phase; -1 when the phase is not on the stepper. */
export function upgradeStepIndex(phase: UpgradePhase): number {
  return UPGRADE_STEPS.findIndex((step) => step.phase === phase)
}

/** A human line for a phase, shown above the progress bar. */
export function upgradePhaseLabel(phase: UpgradePhase): string {
  switch (phase) {
    case "idle":
      return "No upgrade has run"
    case "queued":
      return "Queued"
    case "checking":
      return "Checking the release"
    case "downloading":
      return "Downloading"
    case "installing":
      return "Installing"
    case "restarting":
      return "Restarting the panel"
    case "health":
      return "Waiting for the new panel"
    case "done":
      return "Upgrade complete"
    case "up_to_date":
      return "Already on the latest version"
    case "rolled_back":
      return "Upgrade failed; the previous version was restored"
    case "failed":
      return "Upgrade failed"
    case "interrupted":
      return "Upgrade stopped reporting"
  }
}

/**
 * How far the current step has got, 0 to 100, or null when the step has no
 * measurable progress (the bar then pulses). Only a download reports bytes.
 */
export function upgradePercent(status: Pick<UpgradeStatus, "phase" | "done" | "total">): number | null {
  if (status.phase !== "downloading" || status.total <= 0) {
    return null
  }
  return Math.min(100, Math.round((status.done / status.total) * 100))
}

/** Bytes as a short human figure: 4.2 MB, 512 KB. */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) {
    return `${bytes} B`
  }
  const units = ["KB", "MB", "GB"]
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toFixed(value >= 10 ? 0 : 1)} ${units[unit]}`
}
