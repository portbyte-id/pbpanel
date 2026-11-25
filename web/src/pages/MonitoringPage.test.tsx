import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { api } from "@/lib/api"
import { MonitoringPage } from "@/pages/MonitoringPage"
import { disksHistory, hostHistory } from "@/test/metricsFixtures"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return { ...actual, api: { request: vi.fn() } }
})

const router = vi.hoisted(() => ({ search: {} as Record<string, unknown>, navigate: vi.fn() }))
vi.mock("@tanstack/react-router", () => ({
  useSearch: () => router.search,
  useNavigate: () => router.navigate,
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => <a href={String(to)}>{children}</a>,
}))

const requested = () => vi.mocked(api.request).mock.calls.map((c) => String(c[0]))

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MonitoringPage />
    </QueryClientProvider>,
  )
}

/** The arguments of the last URL write, applied to a previous search. */
function lastSearch(prev: object): object {
  const arg = router.navigate.mock.calls.at(-1)?.[0] as { search: (p: object) => object; replace: boolean }
  expect(arg.replace).toBe(true)
  return arg.search(prev)
}

beforeEach(() => {
  vi.mocked(api.request).mockReset()
  vi.mocked(api.request).mockImplementation(async (path: string) =>
    path.includes("/host") ? hostHistory() : disksHistory(),
  )
  router.search = {}
  router.navigate.mockReset()
})

describe("MonitoringPage", () => {
  it("starts on one hour when the URL names no period", async () => {
    renderPage()
    expect(await screen.findByRole("figure", { name: "CPU" })).toBeInTheDocument()
    expect(screen.getByRole("radio", { name: "1 hour" })).toBeChecked()
    expect(requested()).toContain("/api/metrics/host?range=1h")
  })

  it("takes the preset from the URL", async () => {
    router.search = { range: "7d" }
    renderPage()
    await screen.findByRole("figure", { name: "CPU" })
    expect(screen.getByRole("radio", { name: "7 days" })).toBeChecked()
    expect(requested()).toContain("/api/metrics/host?range=7d")
  })

  it("writes a preset pick to the URL and drops any custom dates", async () => {
    router.search = { from: "2026-11-01", to: "2026-11-10" }
    renderPage()
    await screen.findByRole("figure", { name: "CPU" })
    await userEvent.setup().click(screen.getByRole("radio", { name: "6 hours" }))
    expect(lastSearch({ other: "kept", from: "2026-11-01", to: "2026-11-10" })).toEqual({ other: "kept", range: "6h" })
  })

  it("asks the daemon for a custom window as unix seconds of its local days", async () => {
    router.search = { from: "2026-11-01", to: "2026-11-10" }
    renderPage()
    expect(await screen.findByRole("figure", { name: "CPU" })).toBeInTheDocument()
    const from = Math.floor(new Date(2026, 10, 1).getTime() / 1000)
    const to = Math.floor(new Date(2026, 10, 11).getTime() / 1000)
    expect(requested()).toContain(`/api/metrics/host?from=${from}&to=${to}`)
    expect(screen.getByRole("radio", { name: "1 hour" })).not.toBeChecked()
    expect(screen.getByText("2026-11-01 to 2026-11-10")).toBeInTheDocument()
  })

  it("writes Apply's dates to the URL and drops the preset", async () => {
    renderPage()
    await screen.findByRole("figure", { name: "CPU" })
    const apply = screen.getByRole("button", { name: "Apply" })
    expect(apply).toBeDisabled()

    fireEvent.change(screen.getByLabelText("From"), { target: { value: "2026-11-02" } })
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2026-11-09" } })
    expect(apply).toBeEnabled()
    await userEvent.setup().click(apply)

    expect(lastSearch({ range: "1h", other: "kept" })).toEqual({ other: "kept", from: "2026-11-02", to: "2026-11-09" })
  })

  it("names a start after the end and does not apply it", async () => {
    renderPage()
    await screen.findByRole("figure", { name: "CPU" })
    fireEvent.change(screen.getByLabelText("From"), { target: { value: "2026-11-09" } })
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2026-11-02" } })
    expect(screen.getByRole("alert")).toHaveTextContent("The start date is after the end date.")
    expect(screen.getByRole("button", { name: "Apply" })).toBeDisabled()
    expect(router.navigate).not.toHaveBeenCalled()
  })
})
