/**
 * The open-appsec agent's contract: the availability the Store card and the
 * firewall pages read, and the acknowledgement an install must carry.
 *
 * The tracking warning is never written here. It is one constant on the server
 * (`internal/appsec.Warning`) and the panel renders it verbatim from the API, so
 * the sentence the operator agreed to is the sentence on screen — a copy in this
 * file could drift from it without anyone noticing.
 */

/** What the host can do with the engine, and what the agent is doing. */
export interface AppsecAvailability {
  /** The serving nginx build has the attachment and the agent is installed and active. */
  available: boolean
  /** What is missing when `available` is false. */
  reason: string
  /** The agent's payload is on disk. */
  installed: boolean
  /** systemd reports nano_agent.service running. */
  agent_active: boolean
  /** The installed release, as the vendor's own script reports it. */
  agent_version: string
  /** The agent's verdict on the policy it last loaded ("Success" or "Error"). */
  policy_load: string
  /** The release this panel installs. */
  pinned_version: string
  /**
   * The tracking statement, verbatim. Rendered as-is wherever the engine is
   * offered; never retyped, never paraphrased.
   */
  warning: string
  /** The exact value an install request must send back to prove the warning was read. */
  acknowledgement: string
}

/** A job the install or uninstall route started, alongside the availability. */
export interface AppsecActionResponse extends AppsecAvailability {
  job?: { id: string; kind: string; state: string }
}

/** The job kinds the two routes start, so a page can find its own job in the list. */
export const APPSEC_INSTALL_JOB = "appsec.install"
export const APPSEC_UNINSTALL_JOB = "appsec.uninstall"

/** One sentence for the agent's state, for a badge beside the card's title. */
export function agentState(a: AppsecAvailability): {
  badge: string
  tone: "success" | "warning" | "destructive" | "outline"
  text: string
} {
  if (!a.installed) {
    return {
      badge: "not installed",
      tone: "outline",
      text: "The agent is not installed on this host. Installing it adds the second firewall engine a site may run.",
    }
  }
  if (!a.agent_active) {
    return {
      badge: "stopped",
      tone: "destructive",
      text: "The agent is installed but its unit is not running, so it inspects nothing. Restart nano_agent.service to bring it back.",
    }
  }
  if (a.policy_load !== "" && a.policy_load !== "Success") {
    return {
      badge: `policy ${a.policy_load.toLowerCase()}`,
      tone: "warning",
      text: "The agent is running but did not report the last policy as loaded, so what it inspects may not match the panel's settings.",
    }
  }
  if (!a.available) {
    return {
      badge: "agent up, not usable",
      tone: "warning",
      text: a.reason || "The agent is running, but this host cannot yet run the engine.",
    }
  }
  return {
    badge: "ready",
    tone: "success",
    text: "A site may now choose open-appsec as its firewall engine.",
  }
}

/**
 * Whether the Store card should offer an install: only when nothing is there.
 * An installed agent is upgraded by a panel release, never by the card, so an
 * installed-but-stopped agent offers a restart hint instead of a second install.
 */
export function canInstall(a: AppsecAvailability): boolean {
  return !a.installed
}
