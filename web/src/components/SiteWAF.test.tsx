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

/**
 * The tracking statement the API sends. It is deliberately a value this test
 * owns and not a copy of the server's constant: the point of the assertion is
 * that whatever the API holds is what the screen shows, character for
 * character — including the sentence fragments a paraphrase would tidy up.
 */
const API_WARNING =
  "The open-appsec agent keeps a continuous TLS connection to Check Point's cloud " +
  "(inext-agents.cloud.ngen.checkpoint.com). There is no supported way to turn that connection off."

/** The open-appsec availability a host that can run the engine reports. */
const appsecReady = {
  available: true,
  reason: "",
  installed: true,
  agent_active: true,
  agent_version: "1.1.37-open-source",
  policy_load: "Success",
  pinned_version: "1.1.37",
  warning: API_WARNING,
  acknowledgement: "open-appsec-cloud-connection-accepted",
}

/** The same, on a host that has no agent yet. */
const appsecMissing = {
  ...appsecReady,
  available: false,
  installed: false,
  agent_active: false,
  agent_version: "",
  policy_load: "",
  reason: "the open-appsec agent is not installed; install it from the Store",
}

function respond(
  config: Partial<WafConfig> = {},
  status: WafSiteStatus = available,
  openAppsec: typeof appsecReady | undefined = appsecReady,
): WafSiteResponse {
  return {
    config: { ...DEFAULT_WAF_CONFIG, ...config },
    status,
    ...(openAppsec ? { open_appsec: openAppsec } : {}),
  }
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
        engine: "modsecurity",
        mode: "detect",
        paranoia: 2,
        rate_limit: { enabled: true, requests_per_second: 25, burst: 50 },
        path_blocklist: ["/wp-admin", "/.git"],
        ip_rules: { allow: ["203.0.113.7"], deny: ["198.51.100.0/24"] },
        exclusions: [{ kind: "rule_arg", rule_id: 942100, path: "/comment", arg: "body", note: "markdown editor" }],
        open_appsec: { confidence: "high", exceptions: [] },
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

describe("SiteWAF engine picker", () => {
  const picker = () => screen.getByRole("group", { name: "Inspection engine" })

  it("offers both engines, with ModSecurity chosen for a row that predates the field", async () => {
    vi.mocked(api.siteWaf).mockResolvedValue(respond())
    renderTab()
    const group = await screen.findByRole("group", { name: "Inspection engine" })
    expect(within(group).getByRole("radio", { name: /ModSecurity/ })).toBeChecked()
    expect(within(group).getByRole("radio", { name: /open-appsec/ })).toBeEnabled()
  })

  it("renders the tracking warning inline, from the API string, character for character", async () => {
    // The D5 mark: the choice is never made without the warning on screen, and
    // what is on screen is the server's own sentence — not a paraphrase that a
    // later edit could quietly soften.
    vi.mocked(api.siteWaf).mockResolvedValue(respond())
    renderTab()
    await screen.findByRole("group", { name: "Inspection engine" })

    const mark = screen.getByRole("note", { name: "open-appsec tracking warning" })
    expect(mark).toHaveAttribute("data-verbatim", "open-appsec-warning")
    expect(mark).toHaveTextContent(API_WARNING)
    // Not truncated, not given an ellipsis: the whole string is in the DOM.
    expect(mark.textContent).toBe(API_WARNING)
    // It is inside the picker, so it is read where the choice is made.
    expect(picker()).toContainElement(mark)
  })

  it("disables an engine the host cannot run, says why from the API, and links to the Store", async () => {
    vi.mocked(api.siteWaf).mockResolvedValue(respond({}, available, appsecMissing))
    renderTab()
    const group = await screen.findByRole("group", { name: "Inspection engine" })

    const option = within(group).getByRole("radio", { name: /open-appsec/ })
    expect(option).toBeDisabled()
    expect(within(group).getByText(/unavailable/)).toBeInTheDocument()
    // The reason is the server's, not one the client made up.
    expect(within(group).getByText(appsecMissing.reason, { exact: false })).toBeInTheDocument()
    expect(within(group).getByRole("link", { name: "Open the Store" })).toHaveAttribute("href", "/store")
  })

  it("enables open-appsec and names the agent when the host can run it", async () => {
    vi.mocked(api.siteWaf).mockResolvedValue(respond())
    renderTab()
    const group = await screen.findByRole("group", { name: "Inspection engine" })
    expect(within(group).getByRole("radio", { name: /open-appsec/ })).toBeEnabled()
    expect(within(group).getByText(/Ready on this host \(agent 1\.1\.37-open-source\)/)).toBeInTheDocument()
    expect(within(group).queryByRole("link", { name: "Open the Store" })).not.toBeInTheDocument()
  })

  it("switches the settings and the exceptions to open-appsec's own controls, and saves the choice", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(respond())
    vi.mocked(api.saveSiteWaf).mockResolvedValue(respond({ engine: "open-appsec" }))
    renderTab()

    await user.click(await screen.findByRole("radio", { name: /open-appsec/ }))
    // Paranoia is ModSecurity's control and is gone; confidence is the engine's.
    expect(screen.queryByLabelText("Paranoia level")).not.toBeInTheDocument()
    expect(screen.getByLabelText("Minimum confidence to block")).toHaveValue("high")
    await openTab(user, "Exclusions")
    expect(screen.getByRole("heading", { name: "open-appsec exceptions" })).toBeInTheDocument()
    expect(screen.queryByLabelText("Rule id")).not.toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Save and apply" }))
    await waitFor(() => expect(api.saveSiteWaf).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.saveSiteWaf).mock.calls[0][1]).toMatchObject({
      engine: "open-appsec",
      open_appsec: { confidence: "high", exceptions: [] },
    })
  }, 30_000)

  it("shows what detect and block are called in the agent's own words", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(respond({ engine: "open-appsec" }))
    renderTab()

    await user.click(await screen.findByRole("radio", { name: "Detection only" }))
    expect(screen.getByTestId("appsec-mode-name")).toHaveTextContent("detect-learn")
    await user.click(screen.getByRole("radio", { name: "Blocking" }))
    expect(screen.getByTestId("appsec-mode-name")).toHaveTextContent("prevent-learn")
  })

  it("keeps a stored open-appsec site editable even when its agent has gone", async () => {
    // A site left on an engine whose agent was removed: the picker must not trap
    // the operator on a disabled radio with no way back to ModSecurity.
    vi.mocked(api.siteWaf).mockResolvedValue(respond({ engine: "open-appsec" }, available, appsecMissing))
    renderTab()
    expect(await screen.findByRole("radio", { name: /open-appsec/ })).toBeChecked()
    expect(screen.getByRole("radio", { name: /ModSecurity/ })).toBeEnabled()
  })

  it("refuses to save a stranded open-appsec site into an inspecting mode, and says why", async () => {
    // The server refuses this too (ErrOpenAppsecMissing), so the form must not
    // offer it: only Off can be saved until the agent is back or the engine
    // changes.
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(
      respond({ engine: "open-appsec", mode: "detect" }, available, appsecMissing),
    )
    renderTab()

    expect(await screen.findByRole("radio", { name: "Detection only" })).toBeDisabled()
    expect(screen.getByRole("radio", { name: "Blocking" })).toBeDisabled()
    expect(screen.getByRole("radio", { name: "Off" })).toBeEnabled()
    expect(screen.getByText(/only Off can be saved/)).toBeInTheDocument()

    // Switching back to ModSecurity makes its modes usable again.
    await user.click(screen.getByRole("radio", { name: /ModSecurity/ }))
    expect(screen.getByRole("radio", { name: "Detection only" })).toBeEnabled()
  })
})

describe("SiteWAF open-appsec exceptions", () => {
  /** The exception form's own fields, once open-appsec is the engine. */
  async function openExceptions() {
    const user = userEvent.setup()
    await user.click(await screen.findByRole("tab", { name: "Exclusions" }))
    return user
  }

  it("offers only the two exception kinds the agent can compile, never a path", async () => {
    vi.mocked(api.siteWaf).mockResolvedValue(respond({ engine: "open-appsec" }))
    renderTab()
    await openExceptions()

    const kinds = within(screen.getByLabelText("Exception type")).getAllByRole("option")
    expect(kinds.map((o) => o.textContent)).toEqual(["Source network", "Request parameter"])
    expect(screen.queryByRole("option", { name: /Path/ })).not.toBeInTheDocument()
    // The reason is stated, so its absence is a decision rather than a gap.
    expect(screen.getByText(/Path exceptions are not offered/)).toBeInTheDocument()
  })

  it("adds a source-network exception and sends it", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(respond({ engine: "open-appsec" }))
    vi.mocked(api.saveSiteWaf).mockResolvedValue(respond({ engine: "open-appsec" }))
    renderTab()
    await openExceptions()

    await user.selectOptions(screen.getByLabelText("Exception type"), "source_ip")
    await user.type(screen.getByLabelText("Source network"), "203.0.113.0/24")
    await user.type(screen.getByLabelText("Note (optional)"), "office")
    await user.click(screen.getByRole("button", { name: "Add exception" }))
    expect(screen.getByRole("table", { name: "open-appsec exceptions" })).toHaveTextContent("203.0.113.0/24")

    await user.click(screen.getByRole("button", { name: "Save and apply" }))
    await waitFor(() => expect(api.saveSiteWaf).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.saveSiteWaf).mock.calls[0][1].open_appsec.exceptions).toEqual([
      { kind: "source_ip", path: "", source_ip: "203.0.113.0/24", param: "", note: "office" },
    ])
  }, 30_000)

  it("refuses a bad network or parameter in the place it was typed, and will not save", async () => {
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(respond({ engine: "open-appsec" }))
    renderTab()
    await openExceptions()

    await user.type(screen.getByLabelText("Source network"), "999.1.1.1")
    await user.click(screen.getByRole("button", { name: "Add exception" }))
    expect(screen.getByRole("alert")).toHaveTextContent(/Not an IP address or CIDR range/)

    await user.selectOptions(screen.getByLabelText("Exception type"), "param")
    await user.type(screen.getByLabelText("Parameter name"), "bad name")
    await user.click(screen.getByRole("button", { name: "Add exception" }))
    expect(screen.getByRole("alert")).toHaveTextContent(/Parameter name may use letters/)
    expect(screen.getByRole("button", { name: "Save and apply" })).toBeDisabled()
  })

  it("lists a stored path exception as not supported, and lets it be removed", async () => {
    // A row that still carries the kind: the agent cannot compile it, so the UI
    // says so rather than presenting it as a working control.
    const user = userEvent.setup()
    vi.mocked(api.siteWaf).mockResolvedValue(
      respond({
        engine: "open-appsec",
        open_appsec: {
          confidence: "high",
          exceptions: [{ kind: "path", path: "/api/", source_ip: "", param: "", note: "" }],
        },
      }),
    )
    renderTab()
    await openExceptions()

    const table = screen.getByRole("table", { name: "open-appsec exceptions" })
    expect(within(table).getByText("not supported")).toBeInTheDocument()
    expect(within(table).getByText("does not apply")).toBeInTheDocument()
    // It blocks saving until removed, because the server would refuse it too.
    expect(screen.getByRole("button", { name: "Save and apply" })).toBeDisabled()
    await user.click(within(table).getByRole("button", { name: "Remove Path prefix exception" }))
    expect(screen.getByText("No exceptions.")).toBeInTheDocument()
  })

  it("marks a stored path exception as a problem, so Save says why it is refused", async () => {
    vi.mocked(api.siteWaf).mockResolvedValue(
      respond({
        engine: "open-appsec",
        open_appsec: {
          confidence: "high",
          exceptions: [{ kind: "path", path: "/api/", source_ip: "", param: "", note: "" }],
        },
      }),
    )
    renderTab()
    await openExceptions()
    expect(screen.getByRole("alert")).toHaveTextContent(/Fix the highlighted values to save/)
    expect(screen.getByRole("alert")).toHaveTextContent(/Path exceptions are not supported/)
  })
})
