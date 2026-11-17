/**
 * The dashboard's alerts, derived from endpoints the panel already has. Each
 * alert names the data it came from; a signal with no endpoint is not made up.
 *
 * Not available: a list of failed background jobs. `/api/jobs/{id}/events` is
 * the only jobs route and it needs an id, so the page cannot enumerate them.
 * Failed actions in the audit trail and failed cron runs are shown instead,
 * under their own names.
 */

import type { AuditEntry, CertView, FirewallState, SiteDetail } from "@/lib/api"
import { isBadStatus, STATUS_LABELS, type CronJob } from "@/lib/cron"
import type { ServiceRow } from "@/lib/sysinfo"

export type AlertSeverity = "error" | "warning"

/** Where the operator goes to deal with it. The page turns this into a typed router link. */
export type AlertLink =
  | { kind: "tls" }
  | { kind: "firewall" }
  | { kind: "site"; domain: string }
  | { kind: "cron" }
  | { kind: "audit" }

export interface DashboardAlert {
  id: string
  severity: AlertSeverity
  source: string
  title: string
  detail?: string
  link?: AlertLink
}

export interface AlertInputs {
  certs?: CertView[]
  firewall?: FirewallState
  siteDetails?: SiteDetail[]
  cron?: CronJob[]
  audit?: AuditEntry[]
  services?: ServiceRow[]
  /** Epoch milliseconds; the audit window ends here. Defaults to now. */
  now?: number
}

/** A certificate this close to its end that the renewal has not dealt with is worth a line. */
export const EXPIRY_WARNING_DAYS = 14
export const AUDIT_WINDOW_MS = 24 * 3_600_000

export function certAlerts(certs: CertView[]): DashboardAlert[] {
  const out: DashboardAlert[] = []
  for (const c of certs) {
    if (c.last_renewal_result === "failed") {
      out.push({
        id: `cert-renewal:${c.domain}`,
        severity: "error",
        source: "Certificates",
        title: `Renewal failed for ${c.domain}`,
        detail: c.last_renewal_error || undefined,
        link: { kind: "tls" },
      })
    }
    if (c.days_left < 0) {
      out.push({
        id: `cert-expired:${c.domain}`,
        severity: "error",
        source: "Certificates",
        title: `The certificate for ${c.domain} has expired`,
        link: { kind: "tls" },
      })
    } else if (c.days_left <= EXPIRY_WARNING_DAYS) {
      out.push({
        id: `cert-expiring:${c.domain}`,
        severity: "warning",
        source: "Certificates",
        title: `The certificate for ${c.domain} expires in ${c.days_left} ${c.days_left === 1 ? "day" : "days"}`,
        link: { kind: "tls" },
      })
    }
  }
  return out
}

export function featureAlerts(sites: SiteDetail[]): DashboardAlert[] {
  const out: DashboardAlert[] = []
  for (const s of sites) {
    if (s.site_file_missing) {
      out.push({
        id: `site-missing:${s.domain}`,
        severity: "error",
        source: "Sites",
        title: `The site file for ${s.domain} is missing`,
        link: { kind: "site", domain: s.domain },
      })
      continue
    }
    const detached = s.features.filter((f) => !f.attached)
    if (detached.length > 0) {
      out.push({
        id: `site-detached:${s.domain}`,
        severity: "warning",
        source: "Sites",
        title: `${s.domain}: ${detached.map((f) => f.feature).join(", ")} detached from the site file`,
        detail: detached.find((f) => f.reason)?.reason,
        link: { kind: "site", domain: s.domain },
      })
    }
  }
  return out
}

export function firewallAlerts(fw: FirewallState): DashboardAlert[] {
  const link: AlertLink = { kind: "firewall" }
  if (fw.pending) {
    return [
      {
        id: "firewall-pending",
        severity: "warning",
        source: "Firewall",
        title: "A firewall change is waiting to be kept or reverted",
        detail: "Without an answer the previous rules return when the timer ends.",
        link,
      },
    ]
  }
  if (!fw.enabled) {
    return [
      {
        id: "firewall-disabled",
        severity: "warning",
        source: "Firewall",
        title: "The firewall is disabled",
        detail: "Every port this server listens on is reachable unless something else filters it.",
        link,
      },
    ]
  }
  if (fw.dirty) {
    return [
      {
        id: "firewall-dirty",
        severity: "warning",
        source: "Firewall",
        title: "Saved firewall rules differ from the live ones",
        link,
      },
    ]
  }
  return []
}

export function cronAlerts(jobs: CronJob[]): DashboardAlert[] {
  return jobs
    .filter((j) => j.last_run !== null && isBadStatus(j.last_run.status))
    .map((j) => ({
      id: `cron:${j.id}`,
      severity: "error" as const,
      source: "Cron",
      title: `${j.name}: last run ${STATUS_LABELS[j.last_run!.status].toLowerCase()}`,
      link: { kind: "cron" as const },
    }))
}

/** Failed actions in the audit trail over the window. One line, not one per action. */
export function auditAlerts(entries: AuditEntry[], now: number): DashboardAlert[] {
  const failed = entries.filter(
    (e) => e.result === "failure" && now - Date.parse(e.at) <= AUDIT_WINDOW_MS,
  )
  if (failed.length === 0) return []
  const newest = failed.reduce((a, b) => (Date.parse(b.at) > Date.parse(a.at) ? b : a))
  return [
    {
      id: "audit-failures",
      severity: "warning",
      source: "Audit log",
      title: `${failed.length} failed ${failed.length === 1 ? "action" : "actions"} in the last 24 hours`,
      detail: `Latest: ${newest.action} on ${newest.target}`,
      link: { kind: "audit" },
    },
  ]
}

export function serviceAlerts(services: ServiceRow[]): DashboardAlert[] {
  return services
    .filter((s) => s.state === "failed")
    .map((s) => ({
      id: `service:${s.name}`,
      severity: "error" as const,
      source: "Services",
      title: `${s.name} has failed`,
      detail: s.detail || undefined,
    }))
}

/** Every alert the inputs support, errors before warnings; a missing input contributes nothing. */
export function deriveAlerts(inputs: AlertInputs): DashboardAlert[] {
  const now = inputs.now ?? Date.now()
  const all = [
    ...serviceAlerts(inputs.services ?? []),
    ...cronAlerts(inputs.cron ?? []),
    ...certAlerts(inputs.certs ?? []),
    ...featureAlerts(inputs.siteDetails ?? []),
    ...(inputs.firewall ? firewallAlerts(inputs.firewall) : []),
    ...auditAlerts(inputs.audit ?? [], now),
  ]
  const rank = (a: DashboardAlert) => (a.severity === "error" ? 0 : 1)
  return all
    .map((a, i) => ({ a, i }))
    .sort((x, y) => rank(x.a) - rank(y.a) || x.i - y.i)
    .map((x) => x.a)
}
