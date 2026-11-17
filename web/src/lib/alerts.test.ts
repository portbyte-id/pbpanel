import { describe, expect, it } from "vitest"

import { deriveAlerts } from "@/lib/alerts"
import type { AuditEntry, CertView, FirewallState, SiteDetail } from "@/lib/api"
import type { CronJob } from "@/lib/cron"

const NOW = Date.parse("2026-11-17T12:00:00Z")

const cert = (over: Partial<CertView>): CertView => ({
  domain: "a.test",
  source: "http01",
  names: ["a.test"],
  not_before: "",
  not_after: "",
  issuer: "",
  serial: "",
  days_left: 60,
  last_renewal_at: null,
  last_renewal_result: "",
  last_renewal_error: "",
  redirect_http: false,
  provider: "",
  ...over,
})

const fw = (over: Partial<FirewallState> = {}): FirewallState => ({
  enabled: true,
  ipv6: true,
  dirty: false,
  sshPorts: [22],
  panelPort: 8443,
  pending: null,
  dockerPorts: [],
  builtins: [],
  ...over,
})

const site = (over: Partial<SiteDetail> = {}): SiteDetail => ({
  domain: "a.test",
  aliases: [],
  kind: "static",
  php_version: null,
  created_at: "",
  site_file: "/x",
  site_file_missing: false,
  features: [],
  ...over,
})

const job = (over: Partial<CronJob> = {}): CronJob => ({
  id: 1,
  name: "backup",
  schedule: "0 3 * * *",
  kind: "argv",
  user: "root",
  timeout_seconds: 3600,
  overlap: "skip",
  retention: 50,
  enabled: true,
  created_at: "",
  updated_at: "",
  next_run: null,
  last_run: null,
  ...over,
})

const audit = (over: Partial<AuditEntry>): AuditEntry => ({
  id: 1,
  at: "2026-11-17T11:00:00Z",
  actor: "admin",
  action: "POST /api/x",
  target: "x",
  result: "failure",
  detail: null,
  ...over,
})

describe("deriveAlerts", () => {
  it("says nothing for a healthy server and for inputs that were not read", () => {
    expect(
      deriveAlerts({ now: NOW, certs: [cert({})], firewall: fw(), siteDetails: [site()], cron: [job()] }),
    ).toEqual([])
    expect(deriveAlerts({ now: NOW })).toEqual([])
  })

  it("flags a failed renewal with the error, and expired or expiring certificates", () => {
    const alerts = deriveAlerts({
      now: NOW,
      certs: [
        cert({ domain: "bad.test", last_renewal_result: "failed", last_renewal_error: "dns: zone not found" }),
        cert({ domain: "old.test", days_left: -2 }),
        cert({ domain: "soon.test", days_left: 1 }),
      ],
    })
    expect(alerts.map((a) => a.id)).toEqual([
      "cert-renewal:bad.test",
      "cert-expired:old.test",
      "cert-expiring:soon.test",
    ])
    expect(alerts[0]?.detail).toBe("dns: zone not found")
    expect(alerts[2]?.title).toMatch(/1 day$/)
    expect(alerts[2]?.severity).toBe("warning")
  })

  it("names detached features and a missing site file", () => {
    const alerts = deriveAlerts({
      now: NOW,
      siteDetails: [
        site({
          domain: "a.test",
          features: [
            { feature: "tls", include: "tls.conf", attached: false, reason: "include line removed" },
            { feature: "acme", include: "acme.conf", attached: true },
          ],
        }),
        site({ domain: "gone.test", site_file_missing: true }),
      ],
    })
    expect(alerts.find((a) => a.id === "site-detached:a.test")).toMatchObject({
      title: "a.test: tls detached from the site file",
      detail: "include line removed",
      link: { kind: "site", domain: "a.test" },
    })
    expect(alerts.find((a) => a.id === "site-missing:gone.test")?.severity).toBe("error")
  })

  it("reports firewall pending before disabled before unapplied, one alert only", () => {
    const pending = { token: "t", startedAt: "", deadline: "" }
    expect(deriveAlerts({ now: NOW, firewall: fw({ pending, dirty: true }) }).map((a) => a.id)).toEqual([
      "firewall-pending",
    ])
    expect(deriveAlerts({ now: NOW, firewall: fw({ enabled: false }) }).map((a) => a.id)).toEqual([
      "firewall-disabled",
    ])
    expect(deriveAlerts({ now: NOW, firewall: fw({ dirty: true }) }).map((a) => a.id)).toEqual([
      "firewall-dirty",
    ])
  })

  it("counts failed audit actions in the last 24 hours only", () => {
    const alerts = deriveAlerts({
      now: NOW,
      audit: [
        audit({ id: 1, at: "2026-11-17T11:00:00Z", action: "PUT /api/php", target: "php:8.4" }),
        audit({ id: 2, at: "2026-11-17T10:00:00Z" }),
        audit({ id: 3, at: "2026-11-15T10:00:00Z" }),
        audit({ id: 4, at: "2026-11-17T11:30:00Z", result: "success" }),
      ],
    })
    expect(alerts).toHaveLength(1)
    expect(alerts[0]?.title).toBe("2 failed actions in the last 24 hours")
    expect(alerts[0]?.detail).toBe("Latest: PUT /api/php on php:8.4")
  })

  it("flags cron jobs whose last run failed, not ones that were skipped", () => {
    const run = (status: NonNullable<CronJob["last_run"]>["status"]) => ({
      id: 1,
      source: "schedule" as const,
      status,
      exit_code: 1,
      started_at: "",
    })
    const alerts = deriveAlerts({
      now: NOW,
      cron: [
        job({ id: 1, name: "bad", last_run: run("failed") }),
        job({ id: 2, name: "late", last_run: run("timeout") }),
        job({ id: 3, name: "fine", last_run: run("skipped") }),
      ],
    })
    expect(alerts.map((a) => a.title)).toEqual(["bad: last run failed", "late: last run timed out"])
  })

  it("puts errors before warnings", () => {
    const alerts = deriveAlerts({
      now: NOW,
      firewall: fw({ enabled: false }),
      cron: [job({ last_run: { id: 1, source: "manual", status: "failed", exit_code: 1, started_at: "" } })],
    })
    expect(alerts.map((a) => a.severity)).toEqual(["error", "warning"])
  })
})
