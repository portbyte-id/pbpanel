import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { api, ApiError } from "@/lib/api"
import type { TrafficPoint, TrafficRequest, TrafficSummary, WafGlobalResponse, WafSources } from "@/lib/waf"
import { pollWhileVisible } from "@/lib/wafQueries"
import { WafPage } from "@/pages/WafPage"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      sites: vi.fn(),
      wafOverview: vi.fn(),
      wafTraffic: vi.fn(),
      wafRequests: vi.fn(),
      wafEventSummary: vi.fn(),
      wafSources: vi.fn(),
      wafGlobal: vi.fn(),
      saveWafGlobal: vi.fn(),
      wafGeo: vi.fn(),
      downloadWafGeo: vi.fn(),
    },
  }
})

const point = (t: number, over: Partial<TrafficPoint> = {}): TrafficPoint => ({
  t,
  requests: 10,
  s2xx: 8,
  s3xx: 1,
  s4xx: 1,
  s5xx: 0,
  bytes: 1000,
  avg_ms: 12,
  waf_events: 0,
  ...over,
})

const summary = (over: Partial<TrafficSummary> = {}): TrafficSummary => ({
  from: 1_700_000_000,
  to: 1_700_000_900,
  step_seconds: 10,
  rps_now: 4.5,
  totals: {
    requests: 12_345,
    bytes: 5_000_000,
    s2xx: 11_000,
    s3xx: 300,
    s4xx: 1_000,
    s5xx: 45,
    waf_events: 7,
    avg_ms: 23.4,
    max_ms: 910,
  },
  series: [point(1_700_000_000), point(1_700_000_010, { s5xx: 2, waf_events: 3 })],
  top_ips: [{ ip: "203.0.113.9", requests: 900, errors: 12, bytes: 1_000_000 }],
  top_paths: [{ path: "/wp-login.php", requests: 700, errors: 690 }],
  top_statuses: [{ status: 200, requests: 11_000 }],
  top_agents: [{ agent: "curl/8.5", requests: 50 }],
  sites: [
    { domain: "a.test", requests: 10_000, errors: 100, waf_events: 5 },
    { domain: "b.test", requests: 2_345, errors: 20, waf_events: 2 },
  ],
  ...over,
})

const req = (seq: number, over: Partial<TrafficRequest> = {}): TrafficRequest => ({
  seq,
  time: "2026-11-18T10:00:00Z",
  site: "a.test",
  ip: "203.0.113.9",
  method: "GET",
  path: `/p/${seq}`,
  status: 200,
  bytes: 512,
  ms: 7,
  referer: "",
  agent: "curl/8.5",
  request_id: `r${seq}`,
  ...over,
})

const sources = (over: Partial<WafSources> = {}): WafSources => ({
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

const globalFixture = (): WafGlobalResponse => ({
  global: {
    bot_block: { enabled: false, empty_agent: false, scanners: true, ai_crawlers: false, seo_crawlers: false, custom: [] },
    sensitive_files: false,
    block_trace: false,
    ip_deny: [],
    error_page: { enabled: false, html: "<p>Blocked <!--#echo var=\"request_id\" --></p>" },
  },
  default_html: "<p>Default <!--#echo var=\"request_id\" --></p>",
  presets: { scanners: ["sqlmap", "nikto"], ai_crawlers: ["GPTBot"], seo_crawlers: ["AhrefsBot"] },
})

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <WafPage />
    </QueryClientProvider>,
  )
}

function setHidden(hidden: boolean) {
  Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden })
}

beforeEach(() => {
  setHidden(false)
  vi.mocked(api.sites).mockResolvedValue([
    { domain: "a.test", aliases: [], kind: "static", root: "/www/a", php_version: null, created_at: "2026-01-01T00:00:00Z" },
    { domain: "b.test", aliases: [], kind: "static", root: "/www/b", php_version: null, created_at: "2026-01-01T00:00:00Z" },
  ] as never)
  vi.mocked(api.wafOverview).mockResolvedValue({
    available: true,
    flavor: "pqc",
    reason: "",
    sites: [
      { domain: "a.test", mode: "block", paranoia: 2, rate_limit: true, path_blocklist: 0, ip_rules: 0, exclusions: 0 },
      { domain: "b.test", mode: "off", paranoia: 1, rate_limit: false, path_blocklist: 0, ip_rules: 0, exclusions: 0 },
    ],
  })
  vi.mocked(api.wafTraffic).mockResolvedValue(summary())
  vi.mocked(api.wafSources).mockResolvedValue(sources())
  vi.mocked(api.wafGlobal).mockResolvedValue(globalFixture())
  vi.mocked(api.wafRequests).mockResolvedValue({ requests: [req(2), req(1)], latest_seq: 2 })
  vi.mocked(api.wafEventSummary).mockResolvedValue({
    top_rules: [
      { rule_id: 942100, message: "SQL Injection Attack", severity: "CRITICAL", count: 5, last: "2026-11-18T10:00:00Z" },
    ],
    by_site: [{ domain: "a.test", count: 7 }],
    by_action: { blocked: 4, detected: 3 },
  })
})

afterEach(() => {
  vi.useRealTimers()
  vi.mocked(api.wafTraffic).mockReset()
  vi.mocked(api.wafSources).mockReset()
  vi.mocked(api.wafRequests).mockReset()
  setHidden(false)
})

describe("pollWhileVisible", () => {
  it("polls at the interval while the tab is shown and stops while it is hidden", () => {
    const interval = pollWhileVisible(2000)
    expect(interval()).toBe(2000)
    setHidden(true)
    expect(interval()).toBe(false)
  })
})

describe("WafPage", () => {
  it("puts the page root in a containing block so sr-only captions cannot escape main", async () => {
    // Each table's caption is `sr-only`: absolutely positioned, so without a
    // positioned ancestor it lays out against the initial containing block,
    // past `main`'s clip, and grows the document taller than the viewport — a
    // second scrollbar beside the app shell's. `relative` on the page root keeps
    // them inside. jsdom has no layout, so this pins the class.
    renderPage()
    await screen.findByText("Requests per second")
    const root = screen.getByText("Requests per second").closest("div.relative")
    expect(root).not.toBeNull()
    for (const table of screen.getAllByRole("table")) {
      if (table.querySelector("caption.sr-only")) expect(root!.contains(table)).toBe(true)
    }
  })

  it("shows the totals, the chart, the sites and the top lists under their tabs", async () => {
    const user = userEvent.setup()
    renderPage()

    const label = await screen.findByText("Requests per second")
    expect(label.closest("div")).toHaveTextContent("4.5")
    expect(screen.getByText("12.3k")).toBeInTheDocument() // requests figure, compacted
    expect(screen.getByText("23 ms")).toBeInTheDocument()
    expect(screen.getByText("slowest 910 ms")).toBeInTheDocument()

    expect(screen.getByRole("img", { name: /Requests by status class .* 20 requests/ })).toBeInTheDocument()
    // The data behind the chart is a table as well.
    expect(screen.getByRole("table", { name: /Requests per 10 seconds by status class/ })).toBeInTheDocument()

    const sitesTable = await screen.findByRole("table", { name: "Traffic per site" })
    const rowA = within(sitesTable).getByRole("row", { name: /a\.test/ })
    expect(within(rowA).getByText(/Blocking · level 2/)).toBeInTheDocument()
    const rowB = within(sitesTable).getByRole("row", { name: /b\.test/ })
    expect(within(rowB).getByText("Off")).toBeInTheDocument()

    // The top lists are behind their own tab, so they are not in the Overview.
    expect(screen.queryByRole("region", { name: "Top client IPs" })).not.toBeInTheDocument()
    await user.click(screen.getByRole("tab", { name: "Top lists" }))
    expect(within(screen.getByRole("region", { name: "Top client IPs" })).getByText("203.0.113.9")).toBeInTheDocument()
    expect(within(screen.getByRole("region", { name: "Top paths" })).getByText("/wp-login.php")).toBeInTheDocument()
    expect(within(screen.getByRole("region", { name: "User agents" })).getByText("curl/8.5")).toBeInTheDocument()
  })

  it("shows the protection posture above the tabs, and the response mix and firewall activity under theirs", async () => {
    const user = userEvent.setup()
    renderPage()

    const posture = await screen.findByRole("region", { name: "Protection" })
    expect(posture).toHaveTextContent("1 of 2 sites are not protected")
    expect(posture).toHaveTextContent("Blocking · 1")
    expect(posture).toHaveTextContent("Rate limit · 1")

    const mix = screen.getByRole("region", { name: "Response mix" })
    expect(mix).toHaveTextContent("2xx success")
    expect(mix).toHaveTextContent("11,000")
    // The tile comes first; the sites table has a column of the same name.
    expect(screen.getAllByText("Error rate")[0].closest("div")).toHaveTextContent("8.5%")

    await user.click(screen.getByRole("tab", { name: "Firewall" }))
    const fw = await screen.findByRole("region", { name: "Firewall activity" })
    expect(await within(fw).findByText("SQL Injection Attack")).toBeInTheDocument()
    expect(within(fw).getByText("942100")).toBeInTheDocument()
    expect(fw).toHaveTextContent("4 blocked")
    expect(fw).toHaveTextContent("3 detected only")
  })

  it("keeps the page working when the firewall audit is unavailable", async () => {
    const user = userEvent.setup()
    vi.mocked(api.wafEventSummary).mockRejectedValue(new ApiError(503, "unavailable", "no audit log"))
    renderPage()
    await user.click(screen.getByRole("tab", { name: "Firewall" }))
    expect(await screen.findByText(/firewall audit is not available/)).toBeInTheDocument()
    // The posture banner belongs to the Overview tab, so the firewall tab does not show it.
    expect(screen.queryByRole("region", { name: "Protection" })).not.toBeInTheDocument()
  })

  it("narrows to one site from the site table and asks the server for that site", async () => {
    const user = userEvent.setup()
    renderPage()

    await user.click(await screen.findByRole("button", { name: "a.test" }))
    await waitFor(() => expect(api.wafTraffic).toHaveBeenLastCalledWith("a.test", "15m"))
    expect(screen.getByLabelText("Site")).toHaveValue("a.test")
    expect(screen.queryByRole("table", { name: "Traffic per site" })).not.toBeInTheDocument()
  })

  it("says the monitor is unavailable when the server does", async () => {
    vi.mocked(api.wafTraffic).mockRejectedValue(new ApiError(503, "unavailable", "the traffic monitor is not running"))
    renderPage()
    expect(await screen.findByText("The traffic monitor is not available.")).toBeInTheDocument()
    expect(screen.getByText("the traffic monitor is not running")).toBeInTheDocument()
  })

  it("polls every two seconds while visible, and not at all while the tab is hidden", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    renderPage()
    await screen.findByText("12.3k")
    const base = vi.mocked(api.wafTraffic).mock.calls.length

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100)
    })
    expect(vi.mocked(api.wafTraffic).mock.calls.length).toBeGreaterThan(base)

    setHidden(true)
    // Let any poll already scheduled finish, then count over a longer quiet stretch.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100)
    })
    const quiet = vi.mocked(api.wafTraffic).mock.calls.length
    await act(async () => {
      await vi.advanceTimersByTimeAsync(8000)
    })
    expect(vi.mocked(api.wafTraffic).mock.calls.length).toBe(quiet)
  })

  it("grows the live feed from the newest sequence number and can be paused", async () => {
    const user = userEvent.setup()
    vi.useFakeTimers({ shouldAdvanceTime: true })
    renderPage()
    await user.click(screen.getByRole("tab", { name: "Live requests" }))

    const feed = await screen.findByRole("table", { name: /Most recent requests/ })
    expect(within(feed).getAllByRole("row")).toHaveLength(3) // header + 2

    vi.mocked(api.wafRequests).mockResolvedValue({ requests: [req(4, { status: 404 }), req(3)], latest_seq: 4 })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100)
    })
    await waitFor(() => expect(within(screen.getByRole("table", { name: /Most recent requests/ })).getAllByRole("row")).toHaveLength(5))
    expect(vi.mocked(api.wafRequests).mock.calls.some(([q]) => q.after === 2)).toBe(true)
    const rows = within(screen.getByRole("table", { name: /Most recent requests/ })).getAllByRole("row")
    expect(rows[1]).toHaveTextContent("/p/4")
    expect(rows[1]).toHaveTextContent("404")

    await user.click(screen.getByRole("button", { name: "Pause" }))
    expect(screen.getByRole("button", { name: "Resume" })).toHaveAttribute("aria-pressed", "true")
    const callsAtPause = vi.mocked(api.wafRequests).mock.calls.length
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000)
    })
    expect(vi.mocked(api.wafRequests).mock.calls.length).toBe(callsAtPause)
  })

  it("sends the feed filters to the server", async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(screen.getByRole("tab", { name: "Live requests" }))
    await screen.findByRole("table", { name: /Most recent requests/ })

    await user.selectOptions(screen.getByLabelText("Status"), "4xx")
    await waitFor(() => expect(vi.mocked(api.wafRequests).mock.calls.some(([q]) => q.status === "4xx")).toBe(true))
    await user.type(screen.getByLabelText("Client IP"), "203.0.113.9")
    await waitFor(() =>
      expect(vi.mocked(api.wafRequests).mock.calls.some(([q]) => q.ip === "203.0.113.9" && q.status === "4xx")).toBe(true),
    )
  })
})
