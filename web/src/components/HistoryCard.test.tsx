import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { HistoryCard } from "@/components/HistoryCard"
import { ServiceTrend } from "@/components/ServiceTrend"
import { api, ApiError } from "@/lib/api"
import { disksHistory, fill, hostHistory, servicesHistory } from "@/test/metricsFixtures"

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

function route(handlers: { host?: unknown; disks?: unknown }) {
  vi.mocked(api.request).mockImplementation(async (path: string) => {
    const kind = path.includes("/host") ? handlers.host : handlers.disks
    if (kind instanceof Error) throw kind
    return kind
  })
}

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <HistoryCard />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.mocked(api.request).mockReset()
  router.search = {}
  router.navigate.mockReset()
})

describe("HistoryCard", () => {
  it("draws the host and disk charts, each named", async () => {
    route({ host: hostHistory(), disks: disksHistory() })
    renderCard()
    for (const name of ["CPU", "Load average", "Memory and swap", "Network", "Disk used", "Disk IO, all mounts"]) {
      expect(await screen.findByRole("figure", { name })).toBeInTheDocument()
    }
    // One line per mount on the disk chart.
    const legend = screen.getByRole("list", { name: "Disk used legend" })
    expect(within(legend).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["/", "/www"])
  })

  it("starts on one hour and asks the API for it", async () => {
    route({ host: hostHistory(), disks: disksHistory() })
    renderCard()
    await screen.findByRole("figure", { name: "CPU" })
    expect(screen.getByRole("radio", { name: "1 hour" })).toBeChecked()
    expect(vi.mocked(api.request).mock.calls.map((c) => c[0])).toContain("/api/metrics/host?range=1h")
  })

  it("takes the range from the URL and writes a pick back to it", async () => {
    router.search = { range: "7d" }
    route({ host: hostHistory(), disks: disksHistory() })
    renderCard()
    await screen.findByRole("figure", { name: "CPU" })
    expect(screen.getByRole("radio", { name: "7 days" })).toBeChecked()
    expect(vi.mocked(api.request).mock.calls.map((c) => c[0])).toContain("/api/metrics/host?range=7d")

    await userEvent.setup().click(screen.getByRole("radio", { name: "6 hours" }))
    expect(router.navigate).toHaveBeenCalledTimes(1)
    const arg = router.navigate.mock.calls[0][0] as { search: (prev: object) => object; replace: boolean }
    expect(arg.replace).toBe(true)
    expect(arg.search({ other: "kept" })).toEqual({ other: "kept", range: "6h" })
  })

  it("says history starts after the first minute when nothing is recorded yet", async () => {
    route({
      host: hostHistory(3, {
        cpu: fill(3, null),
        load1: fill(3, null),
        mem_used: fill(3, null),
        net_rx: fill(3, null),
      }),
      disks: disksHistory(3),
    })
    renderCard()
    expect(await screen.findByText("History starts recording after the first minute.")).toBeInTheDocument()
    expect(screen.queryByRole("figure")).not.toBeInTheDocument()
  })

  it("explains 409 as recording being off, without an alarm", async () => {
    route({ host: new ApiError(409, "conflict", "metrics history is turned off"), disks: new ApiError(409, "conflict", "off") })
    renderCard()
    const note = await screen.findByText(/History recording is turned off/)
    expect(note).toHaveAttribute("role", "status")
    expect(within(note).getByRole("link", { name: "Settings, Metrics" })).toHaveAttribute("href", "/settings")
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })

  it("explains 503 as the history database being unavailable", async () => {
    route({ host: new ApiError(503, "unavailable", "no db"), disks: new ApiError(503, "unavailable", "no db") })
    renderCard()
    const note = await screen.findByText(/the history database could not be opened/)
    expect(note).toHaveAttribute("role", "status")
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })

  it("keeps the host charts when only the disk history fails", async () => {
    route({ host: hostHistory(), disks: new ApiError(500, "internal", "boom") })
    renderCard()
    expect(await screen.findByRole("figure", { name: "CPU" })).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText("The disk history could not be loaded.")).toBeInTheDocument())
  })
})

describe("ServiceTrend", () => {
  const svc = servicesHistory().services

  it("labels a running service with its CPU peak and uptime", () => {
    render(<ServiceTrend history={svc[0]} t={servicesHistory().t} />)
    expect(screen.getByRole("img", { name: "panel over the last hour, CPU peak 3.0% of a core, running 100% of the time" })).toBeInTheDocument()
  })

  it("shades the stretches a service was down and breaks the line over them", () => {
    const { container } = render(<ServiceTrend history={svc[1]} t={servicesHistory().t} />)
    expect(screen.getAllByTestId("down-band")).toHaveLength(2)
    expect(container.querySelector("path")?.getAttribute("d")?.match(/M/g)).toHaveLength(2)
    expect(screen.getByRole("img")).toHaveAccessibleName(/running 70% of the time/)
  })

  it("shows a dash for a service with no history", () => {
    render(<ServiceTrend history={undefined} t={[]} />)
    expect(screen.getByRole("img", { name: "No history yet" })).toBeInTheDocument()
  })
})
