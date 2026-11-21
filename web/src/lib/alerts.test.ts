import { describe, expect, it } from "vitest"

import { deriveAlerts, EXPIRY_WARNING_DAYS, isIPName, SHORT_LIVED_EXPIRY_WARNING_DAYS } from "@/lib/alerts"
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

describe("isIPName", () => {
  it("accepts IPv4 dotted quads and IPv6 literals, bracketed or not", () => {
    for (const name of [
      "203.0.113.9",
      "0.0.0.0",
      "255.255.255.255",
      "::1",
      "2001:db8::1",
      "2001:0db8:0000:0000:0000:0000:0000:0001",
      "fe80::1",
      "[2001:db8::1]",
      "::ffff:203.0.113.9",
    ]) {
      expect(isIPName(name), name).toBe(true)
    }
  })

  it("rejects names that only look like addresses", () => {
    for (const name of [
      "1.2.3.example",
      "::1.example",
      "a.test",
      "1.2.3",
      "1.2.3.4.5",
      "256.1.1.1",
      "01.2.3.4",
      "2001:db8::1:zzzz",
      "2001:db8::1::2",
      "1:2:3",
      "example.com:443",
      "",
    ]) {
      expect(isIPName(name), name).toBe(false)
    }
  })
})

describe("certificates that name an IP", () => {
  it("pins both windows", () => {
    expect(EXPIRY_WARNING_DAYS).toBe(14)
    expect(SHORT_LIVED_EXPIRY_WARNING_DAYS).toBe(2)
  })

  const ipCert = (over: Partial<CertView> = {}) =>
    cert({ domain: "203.0.113.9", names: ["203.0.113.9"], ...over })

  it("says nothing about a healthy IP certificate six days from its end", () => {
    expect(deriveAlerts({ now: NOW, certs: [ipCert({ days_left: 6 })] })).toEqual([])
  })

  it("warns an IP certificate with two days left, still under the 14-day window", () => {
    const alerts = deriveAlerts({ now: NOW, certs: [ipCert({ days_left: SHORT_LIVED_EXPIRY_WARNING_DAYS })] })
    expect(alerts.map((a) => a.id)).toEqual(["cert-expiring:203.0.113.9"])
    expect(alerts[0]?.severity).toBe("warning")
    expect(alerts[0]?.title).toMatch(/2 days$/)
  })

  it("warns an IP certificate with one day left, singular", () => {
    const alerts = deriveAlerts({ now: NOW, certs: [ipCert({ days_left: 1 })] })
    expect(alerts.map((a) => a.id)).toEqual(["cert-expiring:203.0.113.9"])
    expect(alerts[0]?.title).toMatch(/1 day$/)
  })

  it("keeps the 14-day window for a domain certificate", () => {
    expect(deriveAlerts({ now: NOW, certs: [cert({ days_left: 10 })] }).map((a) => a.id)).toEqual([
      "cert-expiring:a.test",
    ])
    expect(deriveAlerts({ now: NOW, certs: [cert({ days_left: 20 })] })).toEqual([])
  })

  it("treats an IPv6 name as short-lived and a mixed set as short-lived", () => {
    expect(deriveAlerts({ now: NOW, certs: [cert({ names: ["2001:db8::1"], days_left: 5 })] })).toEqual([])
    expect(deriveAlerts({ now: NOW, certs: [cert({ names: ["2001:db8::1"], days_left: 2 })] })).toHaveLength(1)
    expect(deriveAlerts({ now: NOW, certs: [cert({ names: ["a.test", "2001:db8::1"], days_left: 6 })] })).toEqual([])
  })

  it("still reports an expired IP certificate as an error", () => {
    const alerts = deriveAlerts({ now: NOW, certs: [ipCert({ days_left: -1 })] })
    expect(alerts.map((a) => [a.id, a.severity])).toEqual([["cert-expired:203.0.113.9", "error"]])
  })
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
