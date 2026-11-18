import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { api, ApiError } from "@/lib/api"
import type { TrafficPoint, TrafficRequest, TrafficSummary } from "@/lib/waf"
import { pollWhileVisible } from "@/lib/wafQueries"
import { TrafficPage } from "@/pages/TrafficPage"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: { sites: vi.fn(), wafOverview: vi.fn(), wafTraffic: vi.fn(), wafRequests: vi.fn() },
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

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <TrafficPage />
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
  vi.mocked(api.wafRequests).mockResolvedValue({ requests: [req(2), req(1)], latest_seq: 2 })
})

afterEach(() => {
  vi.useRealTimers()
  vi.mocked(api.wafTraffic).mockReset()
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

describe("TrafficPage", () => {
  it("shows the totals, the chart, the tables and the per-site WAF mode", async () => {
    renderPage()

    const label = await screen.findByText("Requests per second now")
    expect(label.closest("div")).toHaveTextContent("4.5")
    expect(screen.getByText("12.3k")).toBeInTheDocument() // requests tile, compacted
    expect(screen.getByText("23 ms")).toBeInTheDocument()
    expect(screen.getByText("slowest 910 ms")).toBeInTheDocument()

    expect(screen.getByRole("img", { name: /Requests by status class .* 20 requests/ })).toBeInTheDocument()
    // The data behind the chart is a table as well.
    expect(screen.getByRole("table", { name: /Requests per 10 seconds by status class/ })).toBeInTheDocument()

    expect(within(screen.getByRole("region", { name: "Top client IPs" })).getByText("203.0.113.9")).toBeInTheDocument()
    expect(within(screen.getByRole("region", { name: "Top paths" })).getByText("/wp-login.php")).toBeInTheDocument()
    expect(within(screen.getByRole("region", { name: "User agents" })).getByText("curl/8.5")).toBeInTheDocument()

    const sitesTable = await screen.findByRole("table", { name: "Traffic per site" })
    const rowA = within(sitesTable).getByRole("row", { name: /a\.test/ })
    expect(within(rowA).getByText(/Blocking · level 2/)).toBeInTheDocument()
    const rowB = within(sitesTable).getByRole("row", { name: /b\.test/ })
    expect(within(rowB).getByText("Off")).toBeInTheDocument()
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
    await screen.findByRole("table", { name: /Most recent requests/ })

    await user.selectOptions(screen.getByLabelText("Status"), "4xx")
    await waitFor(() => expect(vi.mocked(api.wafRequests).mock.calls.some(([q]) => q.status === "4xx")).toBe(true))
    await user.type(screen.getByLabelText("Client IP"), "203.0.113.9")
    await waitFor(() =>
      expect(vi.mocked(api.wafRequests).mock.calls.some(([q]) => q.ip === "203.0.113.9" && q.status === "4xx")).toBe(true),
    )
  })
})
