import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { SiteWAF } from "@/components/SiteWAF"
import { api, ApiError } from "@/lib/api"
import { DEFAULT_WAF_CONFIG, type WafConfig, type WafSiteResponse, type WafSiteStatus } from "@/lib/waf"

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children: React.ReactNode; to: string; className?: string }) => (
    <a href={String(to)} className={className}>
      {children}
    </a>
  ),
}))

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return { ...actual, api: { siteWaf: vi.fn(), saveSiteWaf: vi.fn(), wafOverview: vi.fn() } }
})

const available = { available: true, flavor: "pqc" as const, reason: "", attached: true, crs_version: "4.8.0" }

function respond(config: Partial<WafConfig> = {}, status: WafSiteStatus = available): WafSiteResponse {
  return { config: { ...DEFAULT_WAF_CONFIG, ...config }, status }
}

function renderTab(prefill?: { rule?: number; path?: string }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <SiteWAF domain="a.test" prefill={prefill} />
    </QueryClientProvider>,
  )
}

/** Opens one of the firewall's tabs, as an operator does. */
async function openTab(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.click(await screen.findByRole("tab", { name }))
}

beforeEach(() => {
  vi.mocked(api.siteWaf).mockReset()
  vi.mocked(api.saveSiteWaf).mockReset()
  vi.mocked(api.wafOverview).mockResolvedValue({ available: true, flavor: "pqc", reason: "", sites: [] })
})

describe("SiteWAF", () => {
  it("builds the whole config from the form and sends it in one PUT", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(respond())
    vi.mocked(api.saveSiteWaf).mockResolvedValue(respond())
    renderTab()

    const save = await screen.findByRole("button", { name: "Save and apply" })
    expect(save).toBeDisabled()

    await user.click(screen.getByRole("radio", { name: "Detection only" }))
    await user.selectOptions(screen.getByLabelText("Paranoia level"), "2")

    await openTab(user, "Rate limit and paths")
    await user.click(screen.getByRole("checkbox", { name: /Rate limit each client IP/ }))
    const rps = screen.getByLabelText("Requests per second")
    await user.clear(rps)
    await user.type(rps, "25")
    const burst = screen.getByLabelText("Burst")
    await user.clear(burst)
    await user.type(burst, "50")
    await user.type(screen.getByLabelText("Blocked paths"), "/wp-admin /.git{Enter}")

    await openTab(user, "IP rules")
    await user.type(screen.getByLabelText("Always block these IPs"), "198.51.100.0/24{Enter}")
    await user.type(screen.getByLabelText("Always allow these IPs"), "203.0.113.7{Enter}")

    await openTab(user, "Exclusions")
    await user.selectOptions(screen.getByLabelText("Exclusion type"), "rule_arg")
    await user.type(screen.getByLabelText("Rule id"), "942100")
    await user.type(screen.getByLabelText("Path prefix"), "/comment")
    await user.type(screen.getByLabelText("Argument name"), "body")
    await user.type(screen.getByLabelText("Note (optional)"), "markdown editor")
    await user.click(screen.getByRole("button", { name: "Add exclusion" }))

    expect(save).toBeEnabled()
    await user.click(save)

    await waitFor(() => expect(api.saveSiteWaf).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.saveSiteWaf).mock.calls[0]).toEqual([
      "a.test",
      {
        mode: "detect",
        paranoia: 2,
        rate_limit: { enabled: true, requests_per_second: 25, burst: 50 },
        path_blocklist: ["/wp-admin", "/.git"],
        ip_rules: { allow: ["203.0.113.7"], deny: ["198.51.100.0/24"] },
        exclusions: [{ kind: "rule_arg", rule_id: 942100, path: "/comment", arg: "body", note: "markdown editor" }],
        challenge: DEFAULT_WAF_CONFIG.challenge,
      },
    ])
  }, 30_000)

  it("shows one section at a time, so the firewall does not stack every panel", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(respond())
    renderTab()

    await openTab(user, "IP rules")
    const shown = () =>
      screen.getAllByRole("tabpanel").filter((p) => !p.hasAttribute("hidden")).map((p) => p.textContent)
    expect(shown()).toHaveLength(1)
    expect(shown()[0]).toContain("Always block these IPs")

    await openTab(user, "Exclusions")
    expect(shown()).toHaveLength(1)
    expect(shown()[0]).toContain("Rule exclusions")
  })

  it("keeps an entry typed in one tab when another tab is shown", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(respond())
    renderTab()

    await openTab(user, "IP rules")
    await user.type(screen.getByLabelText("Always block these IPs"), "198.51.100.7")
    await openTab(user, "Challenge")
    await openTab(user, "IP rules")
    expect(screen.getByLabelText("Always block these IPs")).toHaveValue("198.51.100.7")
  })

  it("switches on the proof-of-work challenge with an exempt path", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(respond())
    vi.mocked(api.saveSiteWaf).mockResolvedValue(respond())
    renderTab()

    await openTab(user, "Challenge")
    await user.click(await screen.findByRole("checkbox", { name: /Check visitors before the site opens/ }))
    expect(screen.getByRole("radio", { name: "Proof of work" })).toBeChecked()
    await user.selectOptions(screen.getByLabelText("Puzzle difficulty"), "20")
    await user.selectOptions(screen.getByLabelText("Let a visitor through for"), "1440")
    await user.type(screen.getByLabelText("Never check these paths"), "/api/{Enter}")
    await user.type(screen.getByLabelText("Never check these IPs"), "203.0.113.0/24{Enter}")
    expect(screen.getByText(/drop out of search results/)).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Save and apply" }))
    await waitFor(() => expect(api.saveSiteWaf).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.saveSiteWaf).mock.calls[0][1].challenge).toEqual({
      ...DEFAULT_WAF_CONFIG.challenge,
      enabled: true,
      difficulty: 20,
      clearance_minutes: 1440,
      exempt_paths: ["/api/"],
      exempt_ips: ["203.0.113.0/24"],
    })
  }, 30_000)

  it("points Turnstile at the panel's Settings, and warns while the keys are missing", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(respond({}, { ...available, turnstile_ready: false }))
    vi.mocked(api.saveSiteWaf).mockResolvedValue(respond())
    renderTab()

    await openTab(user, "Challenge")
    await user.click(await screen.findByRole("checkbox", { name: /Check visitors before the site opens/ }))
    await user.click(screen.getByRole("radio", { name: "Cloudflare Turnstile" }))

    expect(screen.queryByLabelText("Turnstile site key")).not.toBeInTheDocument()
    expect(screen.queryByLabelText("Turnstile secret key")).not.toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Settings" })).toHaveAttribute("href", "/settings")
    expect(screen.getByText(/Turnstile is not set up on this panel yet/)).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Save and apply" }))
    await waitFor(() => expect(api.saveSiteWaf).toHaveBeenCalledTimes(1))
    const sent = vi.mocked(api.saveSiteWaf).mock.calls[0][1].challenge
    expect(sent).toMatchObject({ enabled: true, provider: "turnstile" })
    expect(sent).not.toHaveProperty("turnstile_site_key")
    expect(sent).not.toHaveProperty("turnstile_secret")
  }, 30_000)

  it("shows no Turnstile warning once the panel holds both keys", async () => {
    const user = userEvent.setup()
    const stored = { ...DEFAULT_WAF_CONFIG.challenge, enabled: true, provider: "turnstile" as const }
    vi.mocked(api.siteWaf).mockResolvedValue(respond({ challenge: stored }, { ...available, turnstile_ready: true }))
    renderTab()

    await openTab(user, "Challenge")
    expect(await screen.findByText(/shared by every site/)).toBeInTheDocument()
    expect(screen.queryByText(/Turnstile is not set up on this panel yet/)).not.toBeInTheDocument()
  })

  it("says why the challenge cannot be switched on without the panel's key", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(respond({}, { ...available, challenge_available: false }))
    renderTab()
    await openTab(user, "Challenge")
    expect(await screen.findByRole("checkbox", { name: /Check visitors before the site opens/ })).toBeDisabled()
    expect(screen.getByText(/needs the panel's secret key/)).toBeInTheDocument()
  })

  it("refuses an entry the server would refuse, in the place it was typed", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(respond())
    renderTab()

    await openTab(user, "Rate limit and paths")
    await user.type(await screen.findByLabelText("Blocked paths"), "admin{Enter}")
    expect(screen.getByRole("alert")).toHaveTextContent(/admin: Start with \//)
    expect(screen.queryByRole("list", { name: "Blocked paths" })).not.toBeInTheDocument()

    await user.clear(screen.getByLabelText("Blocked paths"))
    await openTab(user, "IP rules")
    await user.type(screen.getByLabelText("Always block these IPs"), "999.1.1.1{Enter}")
    expect(screen.getByText(/Not an IP address or CIDR range/)).toBeInTheDocument()

    await openTab(user, "Exclusions")
    await user.type(screen.getByLabelText("Rule id"), "0")
    await user.click(screen.getByRole("button", { name: "Add exclusion" }))
    expect(screen.getByText(/Rule id must be a whole number/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Save and apply" })).toBeDisabled()
  })

  it("removes a list entry and an exclusion", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(
      respond({
        path_blocklist: ["/a", "/b"],
        exclusions: [{ kind: "rule", rule_id: 920350, path: "", arg: "", note: "" }],
      }),
    )
    renderTab()

    await openTab(user, "Rate limit and paths")
    await user.click(await screen.findByRole("button", { name: "Remove /a" }))
    expect(within(screen.getByRole("list", { name: "Blocked paths" })).getAllByRole("listitem")).toHaveLength(1)
    await openTab(user, "Exclusions")
    await user.click(screen.getByRole("button", { name: /Remove exclusion of rule 920350/ }))
    expect(screen.getByText("No exclusions.")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Save and apply" })).toBeEnabled()
    await user.click(screen.getByRole("button", { name: "Discard changes" }))
    expect(screen.getByRole("button", { name: /Remove exclusion of rule 920350/ })).toBeInTheDocument()
  })

  it("explains why ModSecurity is off and keeps the native protections usable", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(
      respond({}, { available: false, flavor: "standard", reason: "", attached: true, crs_version: "" }),
    )
    renderTab()

    expect(await screen.findByText(/ModSecurity is not available on this server/)).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Runtimes page" })).toHaveAttribute("href", "/runtimes")
    expect(screen.getByRole("radio", { name: "Detection only" })).toBeDisabled()
    expect(screen.getByRole("radio", { name: "Blocking" })).toBeDisabled()
    expect(screen.getByRole("radio", { name: "Off" })).toBeEnabled()
    await openTab(user, "Rate limit and paths")
    expect(screen.getByRole("checkbox", { name: /Rate limit each client IP/ })).toBeEnabled()
  })

  it("asks before switching to blocking, and only then saves", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(respond({ mode: "detect" }))
    vi.mocked(api.saveSiteWaf).mockResolvedValue(respond({ mode: "block" }))
    renderTab()

    await user.click(await screen.findByRole("radio", { name: "Blocking" }))
    await user.click(screen.getByRole("button", { name: "Save and apply" }))

    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByText(/affects live traffic/)).toBeInTheDocument()
    expect(api.saveSiteWaf).not.toHaveBeenCalled()

    await user.click(within(dialog).getByRole("button", { name: "Switch to blocking" }))
    await waitFor(() => expect(api.saveSiteWaf).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.saveSiteWaf).mock.calls[0][1].mode).toBe("block")
  })

  it("shows nginx's own words when the configuration is rejected", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(respond())
    vi.mocked(api.saveSiteWaf).mockRejectedValue(
      new ApiError(422, "config_rejected", "nginx rejected the change; nothing was applied", {
        output: 'nginx: [emerg] unknown directive "modsecurity" in /www/a.conf:12',
      }),
    )
    renderTab()

    await user.click(await screen.findByRole("radio", { name: "Detection only" }))
    await user.click(screen.getByRole("button", { name: "Save and apply" }))

    expect(await screen.findByText("nginx rejected the change; nothing was applied")).toBeInTheDocument()
    expect(screen.getByTestId("validator-output")).toHaveTextContent('unknown directive "modsecurity"')
    // The draft is kept, so the operator can fix it and try again.
    expect(screen.getByRole("radio", { name: "Detection only" })).toBeChecked()
  })

  it("opens with a rule from the audit log ready to add", async () => {
    vi.mocked(api.siteWaf).mockResolvedValue(respond({ mode: "detect" }))
    renderTab({ rule: 942100, path: "/search" })

    expect(await screen.findByText(/Rule 942100 on \/search is filled in below/)).toBeInTheDocument()
    expect(screen.getByRole("tab", { name: "Exclusions" })).toHaveAttribute("data-state", "active")
    expect(screen.getByLabelText("Exclusion type")).toHaveValue("rule_path")
    expect(screen.getByLabelText("Rule id")).toHaveValue(942100)
    expect(screen.getByLabelText("Path prefix")).toHaveValue("/search")
  })

  it("reports a load failure with a retry", async () => {
    vi.mocked(api.siteWaf).mockRejectedValue(new ApiError(503, "unavailable", "nginx is not available on this host"))
    renderTab()
    expect(await screen.findByText("The firewall settings could not be loaded.")).toBeInTheDocument()
    expect(screen.getByText("nginx is not available on this host")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Try again/ })).toBeInTheDocument()
  })
})
