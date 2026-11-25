import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { AlertRules, ClearHistory, hysteresisText } from "@/components/AlertRules"
import { OpenAlertsCard } from "@/components/OpenAlerts"
import { api, ApiError } from "@/lib/api"
import { KINDS, rule } from "@/test/metricsFixtures"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return { ...actual, api: { request: vi.fn() } }
})

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search }: { children: React.ReactNode; to: string; params?: Record<string, string>; search?: Record<string, string> }) => (
    <a href={`${to.replace("$domain", params?.domain ?? "")}${search ? `?${new URLSearchParams(search)}` : ""}`}>{children}</a>
  ),
}))

const builtin = rule({ id: 1, kind: "disk_pct", builtin: true })
const custom = rule({ id: 9, kind: "site_5xx_pct", target: "a.test", threshold: 5, clear_threshold: 2, builtin: false })

function serve(rules = [builtin, custom], status?: object) {
  vi.mocked(api.request).mockImplementation(async (path: string, init?: RequestInit) => {
    if (path === "/api/alerts") return { rules, kinds: KINDS }
    if (path === "/api/metrics/status") return status ?? { enabled: true, file_bytes: 2_097_152, wal_bytes: 0, tiers: [{ table: "host_10s", rows: 1200, oldest: 1 }, { table: "host_1m", rows: 34, oldest: 1 }], last_flush: 0, last_error: "", last_error_at: 0, dropped: 0 }
    if (init?.method === "PUT") return { ...builtin, ...JSON.parse(String(init.body)) }
    return undefined
  })
}

function renderIt(ui: React.ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

const writes = () => vi.mocked(api.request).mock.calls.filter((c) => (c[1] as RequestInit | undefined)?.method)

beforeEach(() => {
  vi.mocked(api.request).mockReset()
})

describe("AlertRules", () => {
  it("lists the rules, with a delete button only on a custom one", async () => {
    serve()
    renderIt(<AlertRules />)
    const table = await screen.findByRole("table", { name: "Threshold alert rules" })
    const rows = within(table).getAllByRole("row").slice(1)
    expect(rows).toHaveLength(2)

    expect(rows[0]).toHaveTextContent("Disk used")
    expect(rows[0]).toHaveTextContent("(built in)")
    expect(rows[0]).toHaveTextContent("≥ 85%, clears below 80%")
    expect(rows[0]).toHaveTextContent("5 min, 5 min")
    expect(within(rows[0]).getByRole("button", { name: "Edit Disk used on *" })).toBeInTheDocument()
    expect(within(rows[0]).queryByRole("button", { name: /^Delete/ })).not.toBeInTheDocument()

    expect(within(rows[1]).getByRole("button", { name: "Delete Site 5xx errors on a.test" })).toBeInTheDocument()
  })

  it("explains hysteresis in a sentence, with the cert_days comparison reversed", async () => {
    expect(hysteresisText("disk_pct")).toMatch(/fires once the value has stayed past the threshold for the "for" time/)
    expect(hysteresisText("disk_pct")).toMatch(/clears once it has stayed past the clear threshold for the "clear for" time/)
    expect(hysteresisText("cert_days")).toMatch(/reversed/)
    expect(hysteresisText("cert_days")).toMatch(/at or below the threshold/)

    serve([rule({ id: 5, kind: "cert_days", target: "*", threshold: 14, clear_threshold: 21 })])
    renderIt(<AlertRules />)
    expect(await screen.findByText("≤ 14 d, clears above 21 d")).toBeInTheDocument()
  })

  it("sends only {enabled} when a switch is toggled", async () => {
    serve()
    const user = userEvent.setup()
    renderIt(<AlertRules />)
    await user.click(await screen.findByRole("switch", { name: "Disk used on *" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    const [path, init] = writes()[0] as [string, RequestInit]
    expect(path).toBe("/api/alerts/rules/1")
    expect(init.method).toBe("PUT")
    expect(JSON.parse(String(init.body))).toEqual({ enabled: false })
  })

  it("edits a rule and sends only the changed fields", async () => {
    serve()
    const user = userEvent.setup()
    renderIt(<AlertRules />)
    await user.click(await screen.findByRole("button", { name: "Edit Disk used on *" }))
    const dialog = await screen.findByRole("dialog")
    const threshold = within(dialog).getByLabelText("Fires at or above")
    await user.clear(threshold)
    await user.type(threshold, "90")
    await user.click(within(dialog).getByRole("button", { name: "Save rule" }))

    await waitFor(() => expect(writes()).toHaveLength(1))
    const [path, init] = writes()[0] as [string, RequestInit]
    expect(path).toBe("/api/alerts/rules/1")
    expect(JSON.parse(String(init.body))).toEqual({ threshold: 90 })
  })

  it("locks a built-in rule's kind and target", async () => {
    serve()
    const user = userEvent.setup()
    renderIt(<AlertRules />)
    await user.click(await screen.findByRole("button", { name: "Edit Disk used on *" }))
    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByLabelText("Kind")).toBeDisabled()
    expect(within(dialog).getByLabelText("Target")).toBeDisabled()
  })

  it("shows a 422 under the field the server named", async () => {
    serve()
    vi.mocked(api.request).mockImplementation(async (path: string, init?: RequestInit) => {
      if (path === "/api/alerts") return { rules: [builtin, custom], kinds: KINDS }
      if (init?.method === "PUT") {
        throw new ApiError(422, "invalid_input", "must be at least the threshold: the alert clears once the value climbs back above it", { field: "clear_threshold" })
      }
      return undefined
    })
    const user = userEvent.setup()
    renderIt(<AlertRules />)
    await user.click(await screen.findByRole("button", { name: "Edit Disk used on *" }))
    const dialog = await screen.findByRole("dialog")
    const clear = within(dialog).getByLabelText("Clears below")
    await user.clear(clear)
    await user.type(clear, "95")
    await user.click(within(dialog).getByRole("button", { name: "Save rule" }))

    const message = await within(dialog).findByText(/must be at least the threshold/)
    expect(message).toHaveAttribute("role", "alert")
    expect(clear).toHaveAttribute("aria-invalid", "true")
    expect(clear).toHaveAccessibleDescription(/must be at least the threshold/)
    // The other fields are not marked.
    expect(within(dialog).getByLabelText("Fires at or above")).not.toHaveAttribute("aria-invalid", "true")
  })

  it("creates a custom rule from the kind's defaults", async () => {
    serve()
    const user = userEvent.setup()
    renderIt(<AlertRules />)
    await user.click(await screen.findByRole("button", { name: "Add rule" }))
    const dialog = await screen.findByRole("dialog")
    await user.selectOptions(within(dialog).getByLabelText("Kind"), "cert_days")
    expect(within(dialog).getByLabelText("Fires at or below")).toHaveValue("14")
    expect(within(dialog).getByLabelText("Clears above")).toHaveValue("21")
    await user.click(within(dialog).getByRole("button", { name: "Add rule" }))

    await waitFor(() => expect(writes()).toHaveLength(1))
    const [path, init] = writes()[0] as [string, RequestInit]
    expect(path).toBe("/api/alerts/rules")
    expect(init.method).toBe("POST")
    expect(JSON.parse(String(init.body))).toMatchObject({ kind: "cert_days", target: "*", threshold: 14, clear_threshold: 21 })
  })

  it("deletes a custom rule only after a confirmation", async () => {
    serve()
    const user = userEvent.setup()
    renderIt(<AlertRules />)
    await user.click(await screen.findByRole("button", { name: "Delete Site 5xx errors on a.test" }))
    expect(writes()).toHaveLength(0)
    await user.click(await screen.findByRole("button", { name: "Delete rule" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0][0]).toBe("/api/alerts/rules/9")
    expect((writes()[0][1] as RequestInit).method).toBe("DELETE")
  })

  it("explains an unavailable history database instead of an empty table", async () => {
    vi.mocked(api.request).mockRejectedValue(new ApiError(503, "unavailable", "no db"))
    renderIt(<AlertRules />)
    expect(await screen.findByText(/Alert rules are not available right now/)).toBeInTheDocument()
  })
})

describe("ClearHistory", () => {
  it("shows the file size and rows next to the button", async () => {
    serve()
    renderIt(<ClearHistory />)
    expect(await screen.findByText("2.00 MiB on disk, 1,234 rows.")).toBeInTheDocument()
  })

  it("asks first, says alert rules are kept, then posts the clear", async () => {
    serve()
    const user = userEvent.setup()
    renderIt(<ClearHistory />)
    await screen.findByText(/on disk/)
    await user.click(screen.getByRole("button", { name: "Clear history" }))
    const dialog = await screen.findByRole("dialog")
    expect(dialog).toHaveTextContent("deletes every stored chart")
    expect(dialog).toHaveTextContent("Alert rules are kept")
    expect(writes()).toHaveLength(0)

    await user.click(within(dialog).getByRole("button", { name: "Delete history" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0][0]).toBe("/api/metrics/clear")
    expect((writes()[0][1] as RequestInit).method).toBe("POST")
  })

  it("is disabled when the history database is unavailable", async () => {
    vi.mocked(api.request).mockRejectedValue(new ApiError(503, "unavailable", "no db"))
    renderIt(<ClearHistory />)
    expect(await screen.findByText("The history database is not available.")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Clear history" })).toBeDisabled()
  })
})

describe("OpenAlertsCard", () => {
  it("renders nothing when no target is open", async () => {
    serve([builtin])
    const { container } = renderIt(<OpenAlertsCard />)
    await waitFor(() => expect(vi.mocked(api.request)).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  it("lists open targets with severity, value and since, and links to the site and the rules", async () => {
    const since = Math.floor(Date.now() / 1000) - 3 * 60
    serve([
      rule({
        id: 9,
        kind: "site_5xx_pct",
        severity: "critical",
        builtin: false,
        states: [
          { target: "a.test", state: "firing", since, value: 12.5 },
          { target: "b.test", state: "pending", since, value: 6 },
        ],
      }),
      builtin,
    ])
    renderIt(<OpenAlertsCard />)
    const card = await screen.findByRole("region", { name: "Threshold alerts" })
    const items = within(card).getAllByRole("listitem")
    expect(items).toHaveLength(2)
    expect(items[0]).toHaveTextContent("Site 5xx errors: a.test")
    expect(items[0]).toHaveTextContent("Firing, now 12.5%, since 3m ago")
    expect(items[0]).toHaveTextContent("critical")
    expect(items[1]).toHaveTextContent("Pending")
    expect(within(items[0]).getByRole("link", { name: "Open metrics" })).toHaveAttribute("href", "/sites/a.test?section=metrics")
    expect(within(card).getByRole("link", { name: "Edit rules" })).toHaveAttribute("href", "/settings?tab=Metrics")
  })
})
