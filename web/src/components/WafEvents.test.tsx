import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { pathOf, WafEvents } from "@/components/WafEvents"
import { api, ApiError } from "@/lib/api"
import type { WafEvent, WafEvents as WafEventsPage } from "@/lib/waf"

const navigate = vi.fn()
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
}))

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      sites: vi.fn(),
      wafEvents: vi.fn(),
      wafEventSummary: vi.fn(),
      wafEventsAbuseIPDBUrl: vi.fn(() => "/api/waf/events/abuseipdb?site=a.test&action=blocked"),
    },
  }
})

const event = (seq: number, over: Partial<WafEvent> = {}): WafEvent => ({
  seq,
  time: "2026-11-18T10:00:00Z",
  site: "a.test",
  ip: "198.51.100.4",
  method: "GET",
  uri: `/search?q=${seq}`,
  status: 403,
  request_id: `req-${seq}`,
  action: "blocked",
  rule_id: 942100,
  message: "SQL Injection Attack Detected via libinjection",
  severity: "CRITICAL",
  matches: [
    {
      rule_id: 942100,
      message: "SQL Injection Attack Detected via libinjection",
      severity: "CRITICAL",
      data: "Matched Data: s&1 found within ARGS:q: 1' or '1'='1",
      tags: ["attack-sqli", "paranoia-level/1"],
      file: "REQUEST-942-APPLICATION-ATTACK-SQLI.conf",
      line: 46,
    },
    {
      rule_id: 949110,
      message: "Inbound Anomaly Score Exceeded (Total Score: 5)",
      severity: "CRITICAL",
      data: "",
      tags: [],
      file: "",
      line: 0,
    },
  ],
  ...over,
})

const page = (events: WafEvent[], over: Partial<WafEventsPage> = {}): WafEventsPage => ({
  events,
  next_before: events.length > 0 ? events[events.length - 1].seq : 0,
  total: events.length,
  ...over,
})

function renderEvents() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <WafEvents />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  navigate.mockReset()
  vi.mocked(api.sites).mockResolvedValue([
    { domain: "a.test", aliases: [], kind: "static", root: "/www/a", php_version: null, created_at: "2026-01-01T00:00:00Z" },
    { domain: "b.test", aliases: [], kind: "static", root: "/www/b", php_version: null, created_at: "2026-01-01T00:00:00Z" },
  ] as never)
  vi.mocked(api.wafEvents).mockReset()
  vi.mocked(api.wafEvents).mockResolvedValue(page([event(3), event(2, { action: "detected", status: 200 })], { total: 2 }))
  vi.mocked(api.wafEventSummary).mockResolvedValue({
    top_rules: [{ rule_id: 942100, message: "SQL Injection Attack Detected via libinjection", severity: "CRITICAL", count: 12, last: 0 }],
    by_site: [{ domain: "a.test", count: 12 }],
    by_action: { blocked: 9, detected: 3 },
  })
})

describe("pathOf", () => {
  it("drops the query string", () => {
    expect(pathOf("/search?q=1")).toBe("/search")
    expect(pathOf("/plain")).toBe("/plain")
  })
})

describe("WafEvents", () => {
  it("lists events with their outcome, and totals from the last hour", async () => {
    renderEvents()

    const table = await screen.findByRole("table", { name: /WAF audit events/ })
    const rows = within(table).getAllByRole("row")
    expect(rows).toHaveLength(3)
    expect(within(rows[1]).getByText("Blocked")).toBeInTheDocument()
    expect(within(rows[2]).getByText("Detected")).toBeInTheDocument()
    expect(within(rows[1]).getByText("942100")).toBeInTheDocument()
    expect(await screen.findByText("9 blocked")).toBeInTheDocument()
    expect(screen.getByText("3 detected")).toBeInTheDocument()
    expect(screen.getByText("Showing 1–2 of 2 events.")).toBeInTheDocument()
  })

  it("offers the filtered set as an AbuseIPDB export", async () => {
    renderEvents()
    const link = await screen.findByRole("link", { name: "Export to AbuseIPDB" })
    expect(link).toHaveAttribute("href", "/api/waf/events/abuseipdb?site=a.test&action=blocked")
    expect(link).toHaveAttribute("download")
  })

  it("passes each filter to the server", async () => {
    const user = userEvent.setup()
    renderEvents()
    await screen.findByRole("table", { name: /WAF audit events/ })

    await user.selectOptions(screen.getByLabelText("Site"), "a.test")
    await user.selectOptions(screen.getByLabelText("Outcome"), "blocked")
    await user.selectOptions(screen.getByLabelText("Severity"), "CRITICAL")
    await user.type(screen.getByLabelText("Rule id"), "942100")
    await user.type(screen.getByLabelText("Client IP"), "198.51.100.4")

    await waitFor(() =>
      expect(vi.mocked(api.wafEvents)).toHaveBeenLastCalledWith(
        expect.objectContaining({
          site: "a.test",
          action: "blocked",
          severity: "CRITICAL",
          rule: "942100",
          ip: "198.51.100.4",
          limit: 50,
        }),
      ),
    )
  })

  it("opens a row to every matched rule, with the request id and matched data", async () => {
    const user = userEvent.setup()
    renderEvents()
    await screen.findByRole("table", { name: /WAF audit events/ })

    expect(screen.queryByRole("list", { name: "Matched rules" })).not.toBeInTheDocument()
    await user.click(screen.getAllByRole("button", { name: /Show details of rule 942100/ })[0])

    const matches = screen.getByRole("list", { name: "Matched rules" })
    expect(within(matches).getAllByRole("listitem")).toHaveLength(2)
    expect(within(matches).getByText("attack-sqli")).toBeInTheDocument()
    expect(within(matches).getByText(/1' or '1'='1/)).toBeInTheDocument()
    expect(within(matches).getByText("Rule 949110")).toBeInTheDocument()
    expect(screen.getByText("req-3")).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: /Hide details of rule 942100/ }))
    expect(screen.queryByRole("list", { name: "Matched rules" })).not.toBeInTheDocument()
  })

  it("hands a rule and its path to the site's Firewall section", async () => {
    const user = userEvent.setup()
    renderEvents()
    await screen.findByRole("table", { name: /WAF audit events/ })

    await user.click(screen.getAllByRole("button", { name: /Show details of rule 942100/ })[0])
    await user.click(screen.getByRole("button", { name: "Exclude rule 942100 on a.test" }))

    expect(navigate).toHaveBeenCalledWith({
      to: "/sites/$domain",
      params: { domain: "a.test" },
      search: { waf_rule: 942100, waf_path: "/search" },
    })
  })

  it("excludes a rule from the summary strip for the only site that has events", async () => {
    const user = userEvent.setup()
    renderEvents()
    await user.click(await screen.findByRole("button", { name: "Exclude rule 942100" }))
    expect(navigate).toHaveBeenCalledWith({
      to: "/sites/$domain",
      params: { domain: "a.test" },
      search: { waf_rule: 942100 },
    })
  })

  it("pages older with the last sequence number as the cursor, and back to newer", async () => {
    const user = userEvent.setup()
    const first = Array.from({ length: 50 }, (_, i) => event(100 - i))
    vi.mocked(api.wafEvents)
      .mockResolvedValueOnce(page(first, { total: 60 }))
      .mockResolvedValueOnce(page([event(10), event(9)], { total: 60, next_before: 0 }))
      .mockResolvedValueOnce(page(first, { total: 60 }))
    renderEvents()

    await screen.findByText("Showing 1–50 of 60 events.")
    expect(screen.getByRole("button", { name: "Newer" })).toBeDisabled()
    await user.click(screen.getByRole("button", { name: "Older" }))

    await screen.findByText("Showing 51–52 of 60 events.")
    expect(vi.mocked(api.wafEvents).mock.calls[1][0]).toEqual(expect.objectContaining({ before: 51, limit: 50 }))
    expect(screen.getByRole("button", { name: "Older" })).toBeDisabled()

    await user.click(screen.getByRole("button", { name: "Newer" }))
    await screen.findByText("Showing 1–50 of 60 events.")
    expect(vi.mocked(api.wafEvents).mock.calls[2][0]).toEqual(expect.objectContaining({ before: undefined }))
  })

  it("explains an empty audit log and an unavailable one", async () => {
    vi.mocked(api.wafEvents).mockResolvedValue(page([], { total: 0 }))
    const { unmount } = renderEvents()
    expect(await screen.findByText(/No WAF events match/)).toBeInTheDocument()
    unmount()

    vi.mocked(api.wafEvents).mockRejectedValue(new ApiError(503, "unavailable", "the WAF monitor is not running"))
    renderEvents()
    expect(await screen.findByText("The WAF audit log is not available.")).toBeInTheDocument()
  })
})
