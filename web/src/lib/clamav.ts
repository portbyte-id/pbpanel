/**
 * ClamAV's contract: the state the Store card reads, and the job the install,
 * uninstall and scan routes start.
 */

/** What the host has: the scanner, its signatures and its two services. */
export interface ClamavAvailability {
  /** The scanner binary is on disk. */
  installed: boolean
  /** ClamAV's version, e.g. "1.4.3"; empty when not installed. */
  version: string
  /** The loaded signature database's version and date; empty until the first download. */
  signature_version: string
  signature_date: string
  /** clamav-daemon is running, so scans are fast. */
  daemon_active: boolean
  /** clamav-freshclam is running, so signatures stay current. */
  updater_active: boolean
  /** A scan started from the panel is running now. */
  scanning: boolean
  /** One sentence on what is wrong or missing; empty when healthy. */
  reason: string
  /** The distribution packages an install adds. */
  packages: string[]
}

/** The job an action started, alongside the availability. */
export interface ClamavActionResponse extends ClamavAvailability {
  job?: { id: string; kind: string; state: string }
}

/** A badge and a sentence for the card's state. */
export function clamavState(a: ClamavAvailability): {
  badge: string
  tone: "success" | "warning" | "destructive" | "outline"
  text: string
} {
  if (!a.installed) {
    return {
      badge: "not installed",
      tone: "outline",
      text: "ClamAV is not installed. Installing it adds an antivirus scanner you can run on any file or folder from the Files page.",
    }
  }
  if (a.signature_version === "") {
    return {
      badge: "no signatures",
      tone: "warning",
      text: a.reason || "ClamAV is installed but has no virus signatures yet, so it cannot scan.",
    }
  }
  if (!a.daemon_active || !a.updater_active) {
    return {
      badge: "degraded",
      tone: "warning",
      text: a.reason || "A ClamAV service is not running.",
    }
  }
  return {
    badge: "ready",
    tone: "success",
    text: "ClamAV is running with current signatures. Right-click a file or folder on the Files page and choose Scan with ClamAV.",
  }
}
