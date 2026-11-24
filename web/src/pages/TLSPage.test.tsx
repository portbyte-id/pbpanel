import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import type { CertInfo, CertView, DNSProvider } from "@/lib/api"
import { matchesQuery } from "@/lib/table"
import { CertificateTable, PanelCertSummary, ProviderList } from "@/pages/TLSPage"

const cert = (over: Partial<CertView>): CertView => ({
  domain: "a.test",
  source: "http01",
  names: ["a.test"],
  not_before: "2026-01-01T00:00:00Z",
  not_after: "2026-04-01T00:00:00Z",
  issuer: "R11",
  serial: "01",
  days_left: 40,
  last_renewal_at: null,
  last_renewal_result: "",
  last_renewal_error: "",
  redirect_http: false,
  provider: "",
  ...over,
})

const siteLink = (d: string) => <a href={`/sites/${d}`}>{d}</a>

describe("CertificateTable", () => {
  it("lists the soonest expiry first", () => {
    render(
      <CertificateTable
        linkedDomains={new Set()}
        renderSiteLink={siteLink}
        certs={[
          cert({ domain: "late.test", not_after: "2026-09-01T00:00:00Z", days_left: 150 }),
          cert({ domain: "soon.test", not_after: "2026-03-01T00:00:00Z", days_left: 9 }),
          cert({ domain: "gone.test", not_after: "2025-12-01T00:00:00Z", days_left: -30 }),
        ]}
      />,
    )
    const rows = screen.getAllByRole("row").slice(1)
    expect(rows.map((r) => r.getAttribute("data-domain"))).toEqual([
      "gone.test",
      "soon.test",
      "late.test",
    ])
    expect(within(rows[0]).getByText("Expired 30 days ago")).toHaveAttribute("data-tone", "expired")
    expect(within(rows[1]).getByText("9 days left")).toHaveAttribute("data-tone", "warn")
    expect(within(rows[2]).getByText("150 days left")).toHaveAttribute("data-tone", "ok")
  })

  it("shows source and renewal status, with the error for a failed renewal", () => {
    render(
      <CertificateTable
        linkedDomains={new Set()}
        renderSiteLink={siteLink}
        certs={[
          cert({ domain: "up.test", source: "upload" }),
          cert({
            domain: "bad.test",
            source: "dns01",
            last_renewal_result: "failed",
            last_renewal_error: "dns: zone not found",
          }),
        ]}
      />,
    )
    expect(within(screen.getByRole("table")).getByText("Uploaded")).toBeInTheDocument()
    expect(screen.getByText("Not renewed by the panel")).toBeInTheDocument()
    expect(screen.getByText("Last renewal failed")).toBeInTheDocument()
    expect(screen.getByText("dns: zone not found")).toBeInTheDocument()
  })

  it("shows no site domain for a certificate in use, and Details opens the linked site", async () => {
    const user = userEvent.setup()
    render(
      <CertificateTable
        linkedDomains={new Set(["shop.test"])}
        renderSiteLink={siteLink}
        certs={[
          cert({ domain: "shop.test", names: ["shop.test", "www.shop.test"], issuer: "R11" }),
          cert({ domain: "free.test" }),
        ]}
      />,
    )
    const linkedRow = document.querySelector('tr[data-domain="shop.test"]') as HTMLElement
    expect(within(linkedRow).queryByText("shop.test")).not.toBeInTheDocument()
    expect(within(linkedRow).getByText("In use by a site")).toBeInTheDocument()
    expect(within(screen.getByRole("row", { name: /free\.test/ })).getByText("free.test")).toBeInTheDocument()

    await user.click(within(linkedRow).getByRole("button", { name: "Details" }))
    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByRole("link", { name: "shop.test" })).toHaveAttribute(
      "href",
      "/sites/shop.test",
    )
    expect(within(dialog).getByText("shop.test, www.shop.test")).toBeInTheDocument()
    expect(within(dialog).getByText("R11")).toBeInTheDocument()
  })

  it("filters by search and source, and pages the list", async () => {
    const user = userEvent.setup()
    const many = Array.from({ length: 12 }, (_, i) =>
      cert({ domain: `host${String(i).padStart(2, "0")}.test`, days_left: 100 + i }),
    )
    render(
      <CertificateTable
        linkedDomains={new Set()}
        renderSiteLink={siteLink}
        certs={[...many, cert({ domain: "upload.test", source: "upload" })]}
      />,
    )
    expect(screen.getByText("1–10 of 13")).toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Next page" }))
    expect(screen.getByText("11–13 of 13")).toBeInTheDocument()

    await user.type(screen.getByRole("searchbox", { name: "Search certificates" }), "upload")
    expect(screen.getByText("1–1 of 1")).toBeInTheDocument()

    await user.clear(screen.getByRole("searchbox", { name: "Search certificates" }))
    await user.selectOptions(screen.getByLabelText("Source"), "upload")
    expect(screen.getByText("1–1 of 1")).toBeInTheDocument()
    await user.selectOptions(screen.getByLabelText("Source"), "dns01")
    expect(screen.getByText("No certificates match the filters.")).toBeInTheDocument()
  })

  it("says how to get the first certificate when there are none", () => {
    render(<CertificateTable certs={[]} linkedDomains={new Set()} renderSiteLink={siteLink} />)
    expect(screen.getByText(/No site has a certificate yet/)).toBeInTheDocument()
    expect(screen.queryByRole("table")).not.toBeInTheDocument()
  })
})

describe("lib/table", () => {
  it("matches every term, ignoring case, across the fields", () => {
    expect(matchesQuery(["Shop.Test", "R11"], "shop r1")).toBe(true)
    expect(matchesQuery(["Shop.Test", "R11"], "shop zzz")).toBe(false)
    expect(matchesQuery([null, 42], "42")).toBe(true)
    expect(matchesQuery(["x"], "   ")).toBe(true)
  })
})

describe("ProviderList", () => {
  const providers: DNSProvider[] = [
    { name: "cf", kind: "cloudflare", updated_at: "2026-01-01T00:00:00Z" },
  ]

  it("shows name and service but nothing that could be a token", () => {
    const { container } = render(
      <ProviderList providers={providers} onReplace={() => {}} onDelete={() => {}} />,
    )
    expect(screen.getByText("cf")).toBeInTheDocument()
    expect(container.querySelector("input")).toBeNull()
    expect(container.textContent).not.toMatch(/api_token|credentials/)
  })

  it("hands the provider's name to replace and delete", async () => {
    const user = userEvent.setup()
    const onReplace = vi.fn()
    const onDelete = vi.fn()
    render(<ProviderList providers={providers} onReplace={onReplace} onDelete={onDelete} />)
    await user.click(screen.getByRole("button", { name: "Replace token" }))
    await user.click(screen.getByRole("button", { name: "Delete" }))
    expect(onReplace).toHaveBeenCalledWith("cf")
    expect(onDelete).toHaveBeenCalledWith("cf")
  })

  it("offers Scan domains for a Cloudflare provider and hands over its name", async () => {
    const user = userEvent.setup()
    const onScan = vi.fn()
    render(
      <ProviderList providers={providers} onReplace={() => {}} onDelete={() => {}} onScan={onScan} />,
    )
    await user.click(screen.getByRole("button", { name: "Scan domains" }))
    expect(onScan).toHaveBeenCalledWith("cf")
  })

  it("shows no scan button without a handler, and disables it while scanning", () => {
    const { rerender } = render(
      <ProviderList providers={providers} onReplace={() => {}} onDelete={() => {}} />,
    )
    expect(screen.queryByRole("button", { name: "Scan domains" })).toBeNull()
    rerender(
      <ProviderList
        providers={providers}
        onReplace={() => {}}
        onDelete={() => {}}
        onScan={() => {}}
        scanning="cf"
      />,
    )
    expect(screen.getByRole("button", { name: "Scanning…" })).toBeDisabled()
  })

  it("says DNS-01 needs a provider when there are none", () => {
    render(<ProviderList providers={[]} onReplace={() => {}} onDelete={() => {}} />)
    expect(screen.getByText(/DNS-01 needs one/)).toBeInTheDocument()
  })
})

describe("PanelCertSummary", () => {
  const info = (over: Partial<CertInfo> = {}): CertInfo => ({
    subject: "CN=panel.a.test",
    sans: ["panel.a.test"],
    issuer: "CN=R11",
    not_before: "2026-01-01T00:00:00Z",
    not_after: "2099-01-01T00:00:00Z",
    serial: "0A",
    self_signed: false,
    ...over,
  })

  it("flags a self-signed certificate and says browsers warn", () => {
    render(<PanelCertSummary cert={info({ self_signed: true })} previousAvailable={false} />)
    expect(screen.getByText("Self-signed")).toBeInTheDocument()
    expect(screen.getByText(/Browsers warn/)).toBeInTheDocument()
  })

  it("does not flag a CA-issued certificate", () => {
    render(<PanelCertSummary cert={info()} previousAvailable />)
    expect(screen.queryByText("Self-signed")).not.toBeInTheDocument()
    expect(screen.getByText("Issued by a CA")).toBeInTheDocument()
    expect(screen.getByText(/previous certificate is still on the server/)).toBeInTheDocument()
  })
})
