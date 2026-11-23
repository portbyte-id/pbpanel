import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { api, ApiError, type SSHAttempt, type SSHAttemptsReport } from "@/lib/api"
import { SSHPage } from "@/pages/SSHPage"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      sshAttempts: vi.fn(),
      sshAbuseIPDBUrl: (range: string) => `/api/ssh/attempts/abuseipdb?range=${range}`,
    },
  }
})

const attempt = (over: Partial<SSHAttempt> = {}): SSHAttempt => ({
  at: "2026-11-21T11:59:00Z",
  ip: "45.155.205.233",
  user: "admin",
  port: 44556,
  outcome: "failed",
  method: "password",
  ...over,
})

const report = (over: Partial<SSHAttemptsReport> = {}): SSHAttemptsReport => ({
  from: "2026-11-20T12:00:00Z",
  to: "2026-11-21T12:00:00Z",
  outcome: "",
  limit: 200,
  total: 2,
  attempts: [
    attempt({ at: "2026-11-21T11:59:00Z", outcome: "success", method: "publickey" }),
    attempt({ at: "2026-11-21T11:58:00Z", outcome: "failed", method: "password", ip: "203.0.113.9", user: "root" }),
  ],
  summary: {
    failed: 7,
    succeeded: 3,
    distinct_ips: 4,
    distinct_users: 5,
    top_ips: [
      { ip: "45.155.205.233", failed: 6, succeeded: 1, total: 7 },
      { ip: "203.0.113.9", failed: 1, succeeded: 0, total: 1 },
    ],
    top_users: [{ user: "root", failed: 7, succeeded: 0, total: 7 }],
  },
  ...over,
})

function setup(result: SSHAttemptsReport | Error = report()) {
  if (result instanceof Error) {
    vi.mocked(api.sshAttempts).mockRejectedValue(result)
  } else {
    vi.mocked(api.sshAttempts).mockResolvedValue(result)
  }
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <SSHPage />
    </QueryClientProvider>,
  )
  return client
}

it("links the AbuseIPDB download for the chosen period", async () => {
  setup()
  const link = await screen.findByRole("link", { name: /download for abuseipdb/i })
  expect(link).toHaveAttribute("href", "/api/ssh/attempts/abuseipdb?range=24h")
  expect(link).toHaveAttribute("download")
  const user = userEvent.setup()
  await user.selectOptions(screen.getByLabelText("Period"), "168h")
  expect(screen.getByRole("link", { name: /download for abuseipdb/i })).toHaveAttribute(
    "href",
    "/api/ssh/attempts/abuseipdb?range=168h",
  )
})

/** The rows of the attempts table only: the two summary tables are separate. */
async function attemptRows() {
  const table = await screen.findByRole("table", { name: /ssh login attempts, newest first/i })
  return within(table).getAllByRole("row").slice(1)
}

describe("SSHPage", () => {
  beforeEach(() => {
    vi.mocked(api.sshAttempts).mockReset()
  })

  it("renders one row per attempt with IP, username, port and time", async () => {
    setup()
    const rows = await attemptRows()
    expect(rows).toHaveLength(2)
    const first = within(rows[0])
    expect(first.getByText("45.155.205.233")).toBeInTheDocument()
    expect(first.getByText("admin")).toBeInTheDocument()
    expect(first.getByText("44556")).toBeInTheDocument()
    // The time cell carries the full ISO value as its tooltip.
    expect(first.getByText(/2026/)).toHaveAttribute("title", "2026-11-21T11:59:00Z")
  })

  it("shows the summary figures the response carries", async () => {
    setup()
    await attemptRows()
    const summary = screen.getByRole("region", { name: /summary for the period/i })
    // The tiles are a definition list: read each label's own value, so a number
    // that also appears in a top list is not ambiguous.
    const tileValue = (label: string) => {
      const dt = within(summary).getByText(label)
      return dt.nextElementSibling?.textContent
    }
    expect(tileValue("Failed attempts")).toBe("7")
    expect(tileValue("Succeeded")).toBe("3")
    expect(tileValue("Distinct IPs")).toBe("4")
    expect(tileValue("Distinct users")).toBe("5")
    // The offender appears in the top-IP list.
    const topIps = within(summary).getByRole("region", { name: /top ips by failures/i })
    expect(within(topIps).getByText("45.155.205.233")).toBeInTheDocument()
  })

  it("marks a failed attempt differently from a succeeded one", async () => {
    setup()
    const rows = await attemptRows()
    expect(within(rows[0]).getByText("Succeeded")).toBeInTheDocument()
    expect(within(rows[0]).queryByText("Failed")).not.toBeInTheDocument()
    expect(within(rows[1]).getByText("Failed")).toBeInTheDocument()
    expect(within(rows[1]).queryByText("Succeeded")).not.toBeInTheDocument()
  })

  it("asks the server for the chosen outcome", async () => {
    setup()
    await attemptRows()
    const user = userEvent.setup()
    await user.selectOptions(screen.getByLabelText("Outcome"), "failed")
    await waitFor(() =>
      expect(vi.mocked(api.sshAttempts)).toHaveBeenLastCalledWith(expect.objectContaining({ outcome: "failed" })),
    )
  })

  it("filters the rows by username without another request", async () => {
    setup()
    await attemptRows()
    const before = vi.mocked(api.sshAttempts).mock.calls.length
    const user = userEvent.setup()
    await user.type(screen.getByLabelText("Username contains"), "root")
    await waitFor(async () => expect(await attemptRows()).toHaveLength(1))
    expect(within((await attemptRows())[0]).getByText("root")).toBeInTheDocument()
    // Typing is a local filter: the journal is not read again for it.
    expect(vi.mocked(api.sshAttempts).mock.calls.length).toBe(before)
  })

  it("explains a 503 unavailable host instead of showing a table", async () => {
    setup(new ApiError(503, "unavailable", "the sshd journal cannot be read on this host"))
    const alert = await screen.findByRole("alert")
    expect(within(alert).getByText(/not available on this host/i)).toBeInTheDocument()
    // The server's own sentence is passed through beneath the explanation.
    expect(within(alert).getByText("the sshd journal cannot be read on this host")).toBeInTheDocument()
    expect(screen.queryByRole("table", { name: /ssh login attempts/i })).not.toBeInTheDocument()
  })

  it("shows an empty state when nothing matches", async () => {
    setup(report({ attempts: [], total: 0 }))
    expect(await screen.findByText(/no attempts match/i)).toBeInTheDocument()
  })
})
