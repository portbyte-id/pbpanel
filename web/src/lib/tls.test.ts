import { describe, expect, it } from "vitest"

import type { CertView } from "@/lib/api"
import {
  canRenew,
  expiryText,
  expiryTone,
  hasPEM,
  issueProblem,
  renewalStatus,
  sortCertsBySoonest,
  splitNames,
} from "@/lib/tls"

const cert = (over: Partial<CertView> = {}): CertView => ({
  domain: "a.test",
  source: "http01",
  names: ["a.test"],
  not_before: "2026-01-01T00:00:00Z",
  not_after: "2026-04-01T00:00:00Z",
  issuer: "R11",
  serial: "01",
  days_left: 30,
  last_renewal_at: null,
  last_renewal_result: "",
  last_renewal_error: "",
  redirect_http: false,
  provider: "",
  ...over,
})

describe("expiryTone", () => {
  it.each([
    [-1, "expired"],
    [-400, "expired"],
    [0, "warn"],
    [1, "warn"],
    [14, "warn"],
    [15, "ok"],
    [89, "ok"],
  ] as const)("%i days left is %s", (days, tone) => {
    expect(expiryTone(days)).toBe(tone)
  })
})

describe("expiryText", () => {
  it("says what the number means at each boundary", () => {
    expect(expiryText(-3)).toBe("Expired 3 days ago")
    expect(expiryText(-1)).toBe("Expired 1 day ago")
    expect(expiryText(0)).toBe("Expires today")
    expect(expiryText(1)).toBe("1 day left")
    expect(expiryText(14)).toBe("14 days left")
  })
})

describe("sortCertsBySoonest", () => {
  it("puts the earliest expiry first without changing the input", () => {
    const input = [
      cert({ domain: "late.test", not_after: "2026-09-01T00:00:00Z" }),
      cert({ domain: "gone.test", not_after: "2025-12-01T00:00:00Z" }),
      cert({ domain: "soon.test", not_after: "2026-03-01T00:00:00Z" }),
    ]
    const before = input.map((c) => c.domain)
    expect(sortCertsBySoonest(input).map((c) => c.domain)).toEqual([
      "gone.test",
      "soon.test",
      "late.test",
    ])
    expect(input.map((c) => c.domain)).toEqual(before)
  })

  it("breaks a tie by domain so the order is stable", () => {
    const same = "2026-03-01T00:00:00Z"
    const sorted = sortCertsBySoonest([
      cert({ domain: "b.test", not_after: same }),
      cert({ domain: "a.test", not_after: same }),
    ])
    expect(sorted.map((c) => c.domain)).toEqual(["a.test", "b.test"])
  })
})

describe("renewal", () => {
  it("cannot renew an uploaded certificate", () => {
    expect(canRenew({ source: "upload" })).toBe(false)
    expect(canRenew({ source: "http01" })).toBe(true)
    expect(canRenew({ source: "dns01" })).toBe(true)
  })

  it("summarises the last renewal", () => {
    expect(renewalStatus(cert({ source: "upload" }))).toBe("Not renewed by the panel")
    expect(renewalStatus(cert({ last_renewal_result: "failed" }))).toBe("Last renewal failed")
    expect(renewalStatus(cert({ last_renewal_result: "ok" }))).toBe("Renewed")
    expect(renewalStatus(cert())).toBe("Not renewed yet")
  })
})

describe("issueProblem", () => {
  it("needs a name", () => {
    expect(issueProblem({ method: "http01", names: [], provider: "" })).toMatch(/at least one name/)
  })

  it("refuses a wildcard over HTTP-01 and names it", () => {
    expect(issueProblem({ method: "http01", names: ["a.test", "*.a.test"], provider: "" })).toMatch(
      /\*\.a\.test is a wildcard/,
    )
  })

  it("allows a wildcard over DNS-01 once a provider is chosen", () => {
    expect(issueProblem({ method: "dns01", names: ["*.a.test"], provider: "" })).toMatch(/provider/)
    expect(issueProblem({ method: "dns01", names: ["*.a.test"], provider: "cf" })).toBeNull()
  })
})

describe("splitNames", () => {
  it("splits, lowercases and removes duplicates", () => {
    expect(splitNames("A.test, www.a.test\n a.test")).toEqual(["a.test", "www.a.test"])
  })
})

describe("hasPEM", () => {
  it("recognises the label variants of a private key", () => {
    expect(hasPEM("-----BEGIN PRIVATE KEY-----\nx", "PRIVATE KEY")).toBe(true)
    expect(hasPEM("-----BEGIN RSA PRIVATE KEY-----\nx", "PRIVATE KEY")).toBe(true)
    expect(hasPEM("-----BEGIN EC PRIVATE KEY-----\nx", "PRIVATE KEY")).toBe(true)
    expect(hasPEM("-----BEGIN CERTIFICATE-----\nx", "PRIVATE KEY")).toBe(false)
  })
})
