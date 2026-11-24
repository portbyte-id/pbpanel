import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { BlockPagePanel } from "@/components/WafBlockPage"
import { ProtectionPanel } from "@/components/WafProtection"
import { SourcesPanel } from "@/components/WafSources"
import { api } from "@/lib/api"
import type { WafGlobalResponse, WafSources } from "@/lib/waf"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      wafGlobal: vi.fn(),
      saveWafGlobal: vi.fn(),
      wafSources: vi.fn(),
      wafGeo: vi.fn(),
      downloadWafGeo: vi.fn(),
    },
  }
})

const globalData = (): WafGlobalResponse => ({
  global: {
    bot_block: { enabled: false, empty_agent: false, scanners: true, ai_crawlers: false, seo_crawlers: false, custom: [] },
    sensitive_files: false,
    block_trace: false,
    ip_deny: ["203.0.113.7"],
    error_page: { enabled: false, html: "<p>Old page <!--#echo var=\"request_id\" --></p>" },
  },
  default_html: "<p>Default page <!--#echo var=\"request_id\" --></p>",
  presets: { scanners: ["sqlmap", "nikto"], ai_crawlers: ["GPTBot"], seo_crawlers: ["AhrefsBot"] },
})

const sourcesData = (over: Partial<WafSources> = {}): WafSources => ({
  range: "15m",
  geo: {
    installed: false,
    month: "",
    entries: 0,
    attribution: "IP Geolocation by DB-IP",
    attribution_url: "https://db-ip.com",
    license_url: "https://creativecommons.org/licenses/by/4.0/",
  },
  requests: 12,
  blocked: 2,
  unlocated: 3,
  untracked: 0,
  countries: [{ code: "US", requests: 9, blocked: 2, addresses: 1 }],
  top_ips: [{ ip: "198.51.100.4", country: "US", requests: 9, errors: 2, blocked: 2 }],
  ...over,
})

function wrap(ui: React.ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.mocked(api.wafGlobal).mockResolvedValue(globalData())
  vi.mocked(api.saveWafGlobal).mockImplementation(async (g) => ({ ...globalData(), global: g }))
  vi.mocked(api.wafSources).mockResolvedValue(sourcesData())
  vi.mocked(api.downloadWafGeo).mockResolvedValue({
    installed: true,
    month: "2026-11",
    entries: 4,
    attribution: "IP Geolocation by DB-IP",
    attribution_url: "https://db-ip.com",
    license_url: "https://creativecommons.org/licenses/by/4.0/",
  })
})

afterEach(() => {
  vi.clearAllMocks()
})

describe("ProtectionPanel", () => {
  it("saves the switches and keeps the block page as it was", async () => {
    wrap(<ProtectionPanel />)
    const user = userEvent.setup()

    await user.click(await screen.findByLabelText(/block bots by their user-agent/i))
    await user.click(screen.getByLabelText(/ai training crawlers/i))
    await user.click(screen.getByLabelText(/refuse dotfiles/i))
    await user.click(screen.getByRole("button", { name: "Save firewall rules" }))

    await waitFor(() => expect(api.saveWafGlobal).toHaveBeenCalledTimes(1))
    const sent = vi.mocked(api.saveWafGlobal).mock.calls[0][0]
    expect(sent.bot_block).toMatchObject({ enabled: true, scanners: true, ai_crawlers: true, custom: [] })
    expect(sent.sensitive_files).toBe(true)
    expect(sent.ip_deny).toEqual(["203.0.113.7"])
    expect(sent.error_page).toEqual(globalData().global.error_page)
  })

  it("refuses a User-Agent name with a space and does not save", async () => {
    wrap(<ProtectionPanel />)
    const user = userEvent.setup()

    await user.click(await screen.findByLabelText(/block bots by their user-agent/i))
    await user.type(screen.getByLabelText(/your own user-agent names/i), "bad agent")
    expect(screen.getByRole("button", { name: "Save firewall rules" })).toBeDisabled()
    expect(screen.getByText(/not allowed: bad agent/i)).toBeInTheDocument()
    expect(api.saveWafGlobal).not.toHaveBeenCalled()
  })
})

describe("BlockPagePanel", () => {
  it("previews the request id as a sample, not the raw directive", async () => {
    wrap(<BlockPagePanel />)
    const frame = await screen.findByTitle("Block page preview")
    const srcDoc = frame.getAttribute("srcdoc") ?? ""
    expect(srcDoc).toContain("Old page")
    expect(srcDoc).not.toContain("<!--#echo")
    expect(srcDoc).toContain("PB-7F3A92C1")
    expect(frame.getAttribute("sandbox")).toBe("")
  })

  it("resets to the default page and saves it with the switch on", async () => {
    wrap(<BlockPagePanel />)
    const user = userEvent.setup()

    await user.click(await screen.findByLabelText(/show this page for refused requests/i))
    await user.click(screen.getByRole("button", { name: "Reset to the default page" }))
    await user.click(screen.getByRole("button", { name: "Save block page" }))

    await waitFor(() => expect(api.saveWafGlobal).toHaveBeenCalledTimes(1))
    const sent = vi.mocked(api.saveWafGlobal).mock.calls[0][0]
    expect(sent.error_page).toEqual({ enabled: true, html: globalData().default_html })
    expect(sent.bot_block).toEqual(globalData().global.bot_block)
  })
})

describe("SourcesPanel", () => {
  it("offers the country database when it is not installed, and downloads it", async () => {
    wrap(<SourcesPanel range="15m" />)
    const user = userEvent.setup()

    const button = await screen.findByRole("button", { name: "Download country database" })
    expect(await screen.findByRole("img", { name: /world map of where requests came from/i })).toBeInTheDocument()
    await user.click(button)
    await waitFor(() => expect(api.downloadWafGeo).toHaveBeenCalledTimes(1))
  })

  it("lists the countries and busiest addresses and credits DB-IP", async () => {
    vi.mocked(api.wafSources).mockResolvedValue(
      sourcesData({
        geo: { ...sourcesData().geo, installed: true, month: "2026-11", entries: 4 },
      }),
    )
    wrap(<SourcesPanel range="15m" />)

    const countries = await screen.findByRole("table", { name: "Requests by country" })
    expect(within(countries).getByText("United States")).toBeInTheDocument()
    expect(within(countries).getByText("9")).toBeInTheDocument()
    const addresses = screen.getByRole("table", { name: "Busiest client addresses" })
    expect(within(addresses).getByText("198.51.100.4")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "IP Geolocation by DB-IP" })).toHaveAttribute("href", "https://db-ip.com")
    expect(screen.getByText(/3 requests came from addresses no country covers/i)).toBeInTheDocument()
  })

  it("shows the error when the sources cannot be read", async () => {
    vi.mocked(api.wafSources).mockRejectedValue(new Error("the monitor is not running"))
    wrap(<SourcesPanel range="15m" />)
    expect(await screen.findByText(/the sources of the traffic are not available/i)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /try again/i })).toBeInTheDocument()
  })
})
