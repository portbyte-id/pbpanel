import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { SiteTLS } from "@/components/SiteTLS"
import { api, ApiError, type CertView, type SiteDetail } from "@/lib/api"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      siteTLS: vi.fn(),
      tlsProviders: vi.fn(),
      uploadSiteCert: vi.fn(),
      issueSiteCert: vi.fn(),
      renewSiteCert: vi.fn(),
      setSiteRedirect: vi.fn(),
      removeSiteCert: vi.fn(),
    },
  }
})

const detail = (tlsAttached = true): SiteDetail => ({
  domain: "a.test",
  aliases: ["www.a.test"],
  kind: "static",
  root: "/www/a",
  php_version: null,
  created_at: "2026-01-01T00:00:00Z",
  site_file: "/etc/nginx/sites/a.test.conf",
  site_file_missing: false,
  features: [
    {
      feature: "tls",
      include: "/etc/nginx/tls/a.test.conf",
      attached: tlsAttached,
      reason: tlsAttached ? undefined : "the site file no longer includes tls/a.test.conf",
    },
  ],
})

const cert = (over: Partial<CertView> = {}): CertView => ({
  domain: "a.test",
  source: "http01",
  names: ["a.test", "www.a.test"],
  not_before: "2026-01-01T00:00:00Z",
  not_after: "2026-04-01T00:00:00Z",
  issuer: "Let's Encrypt R11",
  serial: "01",
  days_left: 40,
  last_renewal_at: null,
  last_renewal_result: "",
  last_renewal_error: "",
  redirect_http: false,
  provider: "",
  ...over,
})

const KEY = "-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----"
const CERT = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"

function renderTab(d: SiteDetail) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <SiteTLS detail={d} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  for (const fn of Object.values(api)) (fn as ReturnType<typeof vi.fn>).mockReset()
  vi.mocked(api.tlsProviders).mockResolvedValue({
    providers: [{ name: "cf", kind: "cloudflare", updated_at: "2026-01-01T00:00:00Z" }],
    kinds: ["cloudflare"],
  })
})

describe("SiteTLS", () => {
  it("says the site is plain HTTP when it has no certificate", async () => {
    vi.mocked(api.siteTLS).mockResolvedValue(null)
    renderTab(detail())
    expect(await screen.findByText(/served over plain HTTP only/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Renew now" })).not.toBeInTheDocument()
    expect(screen.getByRole("heading", { name: /Get a certificate from Let's Encrypt/ })).toBeInTheDocument()
  })

  it("shows issuer, names, source and the days left in the warning tone", async () => {
    vi.mocked(api.siteTLS).mockResolvedValue(cert({ days_left: 10 }))
    renderTab(detail())

    expect(await screen.findByText("Let's Encrypt R11")).toBeInTheDocument()
    expect(screen.getByText("a.test, www.a.test")).toBeInTheDocument()
    expect(screen.getByText("Let's Encrypt (HTTP-01)")).toBeInTheDocument()
    expect(screen.getByText("10 days left")).toHaveAttribute("data-tone", "warn")
  })

  it("marks an expired certificate", async () => {
    vi.mocked(api.siteTLS).mockResolvedValue(cert({ days_left: -2 }))
    renderTab(detail())
    expect(await screen.findByText("Expired 2 days ago")).toHaveAttribute("data-tone", "expired")
  })

  it("shows why the last renewal failed", async () => {
    vi.mocked(api.siteTLS).mockResolvedValue(
      cert({
        last_renewal_result: "failed",
        last_renewal_at: "2026-03-01T00:00:00Z",
        last_renewal_error: "acme: error 403 unauthorized",
      }),
    )
    renderTab(detail())
    expect(await screen.findByText(/Last renewal failed/)).toBeInTheDocument()
    expect(screen.getByText("acme: error 403 unauthorized")).toBeInTheDocument()
  })

  it("disables renewal for an uploaded certificate and gives the reason", async () => {
    vi.mocked(api.siteTLS).mockResolvedValue(cert({ source: "upload" }))
    renderTab(detail())

    const button = await screen.findByRole("button", { name: "Renew now" })
    expect(button).toBeDisabled()
    const why = document.getElementById(button.getAttribute("aria-describedby") ?? "")
    expect(why).toHaveTextContent("can't be renewed here")
  })

  it("starts a renewal job and opens its output", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteTLS).mockResolvedValue(cert())
    vi.mocked(api.renewSiteCert).mockResolvedValue("job-1")
    class FakeEventSource {
      readyState = 0
      addEventListener() {}
      close() {}
    }
    vi.stubGlobal("EventSource", FakeEventSource)

    renderTab(detail())
    await user.click(await screen.findByRole("button", { name: "Renew now" }))

    expect(api.renewSiteCert).toHaveBeenCalledWith("a.test")
    expect(await screen.findByText("job-1")).toBeInTheDocument()
    vi.unstubAllGlobals()
  })

  it("toggles the HTTPS redirect and explains the ACME path", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteTLS).mockResolvedValue(cert())
    vi.mocked(api.setSiteRedirect).mockResolvedValue(cert({ redirect_http: true }))
    renderTab(detail())

    const box = await screen.findByRole("checkbox", { name: "Redirect HTTP to HTTPS" })
    expect(box).not.toBeChecked()
    expect(screen.getByText(/\.well-known\/acme-challenge\/ stay on HTTP/)).toBeInTheDocument()
    await user.click(box)
    expect(api.setSiteRedirect).toHaveBeenCalledWith("a.test", true)
  })

  it("asks before removing, says the site returns to plain HTTP, then removes", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteTLS).mockResolvedValue(cert())
    vi.mocked(api.removeSiteCert).mockResolvedValue(undefined)
    renderTab(detail())

    await user.click(await screen.findByRole("button", { name: "Remove certificate" }))
    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByText(/goes back to plain HTTP/)).toBeInTheDocument()
    expect(api.removeSiteCert).not.toHaveBeenCalled()

    await user.click(within(dialog).getByRole("button", { name: "Remove certificate" }))
    await waitFor(() => expect(api.removeSiteCert).toHaveBeenCalledWith("a.test"))
  })

  it("shows the refusal inside the remove dialog", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteTLS).mockResolvedValue(cert())
    vi.mocked(api.removeSiteCert).mockRejectedValue(
      new ApiError(500, "apply_failed", "nginx rejected the config"),
    )
    renderTab(detail())

    await user.click(await screen.findByRole("button", { name: "Remove certificate" }))
    const dialog = await screen.findByRole("dialog")
    await user.click(within(dialog).getByRole("button", { name: "Remove certificate" }))
    expect(await within(dialog).findByText("nginx rejected the config")).toBeInTheDocument()
  })

  it("uploads, shows the warnings the server returned, and empties the key", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteTLS).mockResolvedValue(null)
    vi.mocked(api.uploadSiteCert).mockResolvedValue({
      cert: cert({ source: "upload" }),
      warnings: [{ code: "no_chain", message: "No intermediate certificate was supplied." }],
    })
    renderTab(detail())

    await user.click(await screen.findByLabelText("Certificate"))
    await user.paste(CERT)
    await user.click(screen.getByLabelText("Private key"))
    await user.paste(KEY)
    await user.click(screen.getByRole("button", { name: "Upload certificate" }))

    expect(api.uploadSiteCert).toHaveBeenCalledWith("a.test", {
      certificate: CERT,
      chain: "",
      private_key: KEY,
    })
    expect(await screen.findByText("No intermediate certificate was supplied.")).toBeInTheDocument()
    expect(screen.getByLabelText("Private key")).toHaveValue("")
  })

  it("offers the site's domain and aliases as the default names", async () => {
    vi.mocked(api.siteTLS).mockResolvedValue(null)
    renderTab(detail())
    expect(await screen.findByLabelText("Names on the certificate")).toHaveValue(
      "a.test www.a.test",
    )
  })
})

describe("SiteTLS when the tls include is detached", () => {
  it("explains why and disables every control", async () => {
    vi.mocked(api.siteTLS).mockResolvedValue(cert())
    renderTab(detail(false))

    expect(await screen.findByText(/Switched off: the site file no longer includes/)).toBeInTheDocument()
    for (const name of [
      "Renew now",
      "Remove certificate",
      "Request certificate",
      "Upload certificate",
    ]) {
      expect(screen.getByRole("button", { name })).toBeDisabled()
    }
    expect(screen.getByRole("checkbox", { name: "Redirect HTTP to HTTPS" })).toBeDisabled()
    expect(screen.getByLabelText("Private key")).toBeDisabled()
    expect(screen.getByLabelText("Names on the certificate")).toBeDisabled()
  })

  it("leaves the controls on when the feature is attached", async () => {
    vi.mocked(api.siteTLS).mockResolvedValue(cert())
    renderTab(detail(true))
    expect(await screen.findByRole("button", { name: "Renew now" })).toBeEnabled()
    expect(screen.queryByText(/Switched off/)).not.toBeInTheDocument()
  })
})
