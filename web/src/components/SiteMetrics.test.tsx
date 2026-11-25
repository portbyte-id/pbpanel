import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, within } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { SiteMetrics, UPSTREAM_PENDING_TEXT } from "@/components/SiteMetrics"
import { api, ApiError } from "@/lib/api"
import { fill, siteHistory } from "@/test/metricsFixtures"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return { ...actual, api: { request: vi.fn() } }
})

vi.mock("@tanstack/react-router", () => ({
  useSearch: () => ({}),
  useNavigate: () => vi.fn(),
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => <a href={String(to)}>{children}</a>,
}))

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <SiteMetrics domain="a.test" />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.mocked(api.request).mockReset()
})

describe("SiteMetrics", () => {
  it("shows requests by status class, bytes, request time and upstream time", async () => {
    vi.mocked(api.request).mockResolvedValue(siteHistory())
    renderSection()
    for (const name of ["Requests by status class", "Bytes sent", "Request time", "Upstream time"]) {
      expect(await screen.findByRole("figure", { name })).toBeInTheDocument()
    }
    expect(vi.mocked(api.request).mock.calls[0][0]).toBe("/api/metrics/sites/a.test?range=1h")

    // The status classes keep the names the traffic chart uses, so colour is not the only cue.
    const legend = screen.getByRole("list", { name: "Requests by status class legend" })
    expect(within(legend).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "2xx success",
      "3xx redirect",
      "4xx client error",
      "5xx server error",
    ])
    expect(screen.queryByTestId("upstream-pending")).not.toBeInTheDocument()
  })

  it("states that the percentiles are estimates good to about 20%, in the footnote and the tooltip", async () => {
    vi.mocked(api.request).mockResolvedValue(siteHistory())
    renderSection()
    const fig = await screen.findByRole("figure", { name: "Request time" })
    expect(within(fig).getByText(/estimates from fixed histogram bins, good to about 20%/)).toBeInTheDocument()
    const p95 = within(within(fig).getByRole("list", { name: "Request time legend" })).getByText("p95")
    expect(p95.closest("li")).toHaveAttribute("title", expect.stringContaining("about 20%"))
  })

  it("replaces the upstream chart with one line when every upstream point is null", async () => {
    vi.mocked(api.request).mockResolvedValue(siteHistory(5, { urt_p50: fill(5, null), urt_p95: fill(5, null) }))
    renderSection()
    expect(await screen.findByTestId("upstream-pending")).toHaveTextContent(UPSTREAM_PENDING_TEXT)
    expect(UPSTREAM_PENDING_TEXT).toBe(
      "Upstream time is not available yet: it appears once nginx has reloaded with the new log format.",
    )
    expect(screen.queryByRole("figure", { name: "Upstream time" })).not.toBeInTheDocument()
    // The other charts are still there.
    expect(screen.getByRole("figure", { name: "Request time" })).toBeInTheDocument()
  })

  it("says history starts after the first minute for a site with no points", async () => {
    vi.mocked(api.request).mockResolvedValue(siteHistory(0))
    renderSection()
    expect(await screen.findByText(/History starts recording after the first minute/)).toBeInTheDocument()
  })

  it("shows the disabled state for 409 and the unavailable state for 503", async () => {
    vi.mocked(api.request).mockRejectedValue(new ApiError(409, "conflict", "off"))
    const first = renderSection()
    expect(await screen.findByText(/History recording is turned off/)).toBeInTheDocument()
    first.unmount()

    vi.mocked(api.request).mockRejectedValue(new ApiError(503, "unavailable", "no db"))
    renderSection()
    expect(await screen.findByText(/history database could not be opened/)).toBeInTheDocument()
  })
})
