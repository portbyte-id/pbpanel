import type { CertView, IssueMethod } from "@/lib/api"

/** How much attention a certificate's expiry needs. */
export type ExpiryTone = "ok" | "warn" | "expired"

/** Days before expiry at which a certificate is flagged. Renewal normally runs well before this. */
export const WARN_DAYS = 14

/**
 * `daysLeft` is the server's whole days to `not_after`. A certificate with 0 days
 * left is valid for a few more hours, so it warns; only a negative count is expired.
 */
export function expiryTone(daysLeft: number): ExpiryTone {
  if (daysLeft < 0) return "expired"
  if (daysLeft <= WARN_DAYS) return "warn"
  return "ok"
}

export function expiryText(daysLeft: number): string {
  if (daysLeft < 0) {
    const ago = -daysLeft
    return `Expired ${ago} ${ago === 1 ? "day" : "days"} ago`
  }
  if (daysLeft === 0) return "Expires today"
  return `${daysLeft} ${daysLeft === 1 ? "day" : "days"} left`
}

export function sourceLabel(source: CertView["source"]): string {
  switch (source) {
    case "upload":
      return "Uploaded"
    case "http01":
      return "Let's Encrypt (HTTP-01)"
    case "dns01":
      return "Let's Encrypt (DNS-01)"
  }
}

/** Uploaded certificates are the operator's to replace; the panel only renews what it issued. */
export function canRenew(cert: Pick<CertView, "source">): boolean {
  return cert.source !== "upload"
}

/** One line on how the certificate gets renewed, for a list row. */
export function renewalStatus(cert: CertView): string {
  if (cert.source === "upload") return "Not renewed by the panel"
  if (cert.last_renewal_result === "failed") return "Last renewal failed"
  if (cert.last_renewal_result === "ok") return "Renewed"
  return "Not renewed yet"
}

/** Soonest expiry first, so the certificate that needs attention is on top. */
export function sortCertsBySoonest(certs: readonly CertView[]): CertView[] {
  return [...certs].sort((a, b) => {
    const byExpiry = Date.parse(a.not_after) - Date.parse(b.not_after)
    return byExpiry !== 0 && !Number.isNaN(byExpiry) ? byExpiry : a.domain.localeCompare(b.domain)
  })
}

/** Names are typed on one line or several; spaces, commas and newlines all separate. */
export function splitNames(text: string): string[] {
  return [
    ...new Set(
      text
        .split(/[\s,]+/)
        .map((s) => s.trim().toLowerCase())
        .filter((s) => s !== ""),
    ),
  ]
}

export function isWildcard(name: string): boolean {
  return name.startsWith("*.")
}

export interface IssueDraft {
  method: IssueMethod
  names: string[]
  provider: string
}

/** The first reason an issuance request cannot be sent, or null. */
export function issueProblem(draft: IssueDraft): string | null {
  if (draft.names.length === 0) return "Enter at least one name."
  if (draft.method === "http01") {
    const wild = draft.names.find(isWildcard)
    if (wild) return `${wild} is a wildcard. Wildcards can only be issued with DNS-01.`
  } else if (draft.provider === "") {
    return "Choose a DNS provider."
  }
  return null
}

/** True when `text` looks like a PEM block with the given label fragment. */
export function hasPEM(text: string, label: string): boolean {
  return new RegExp(`-----BEGIN [A-Z ]*${label}-----`).test(text)
}

export function formatDate(iso: string): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return iso
  return new Date(t).toLocaleDateString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
  })
}
