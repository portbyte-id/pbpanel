import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { api, ApiError } from "@/lib/api"
import {
  classifyHistory,
  hasNoData,
  PERCENTILE_NOTE,
  percentileTooltip,
  rangeFromSearch,
  useHostHistory,
  useSiteHistory,
  type MetricsRange,
} from "@/lib/metrics"
import { hostHistory, siteHistory } from "@/test/metricsFixtures"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return { ...actual, api: { request: vi.fn() } }
})

vi.mock("@tanstack/react-router", () => ({ useSearch: () => ({}), useNavigate: () => vi.fn() }))

const paths = () => vi.mocked(api.request).mock.calls.map((c) => String(c[0]))

function Probe({ range }: { range: MetricsRange }) {
  const q = useHostHistory(range)
  return <p>{q.data ? `points:${q.data.t.length}` : "loading"}</p>
}

function wrap(range: MetricsRange) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const ui = (r: MetricsRange) => (
    <QueryClientProvider client={client}>
      <Probe range={r} />
    </QueryClientProvider>
  )
  const view = render(ui(range))
  return { ...view, again: (r: MetricsRange) => view.rerender(ui(r)) }
}

beforeEach(() => {
  vi.mocked(api.request).mockReset()
})

describe("range", () => {
  it("falls back to one hour for a missing or unknown value", () => {
    expect(rangeFromSearch(undefined)).toBe("1h")
    expect(rangeFromSearch("2h")).toBe("1h")
    expect(rangeFromSearch("90d")).toBe("90d")
  })

  it("asks again with the new range when it changes", async () => {
    vi.mocked(api.request).mockResolvedValue(hostHistory(4))
    const view = wrap("1h")
    expect(await screen.findByText("points:4")).toBeInTheDocument()
    expect(paths()).toEqual(["/api/metrics/host?range=1h"])

    view.again("6h")
    await waitFor(() => expect(paths()).toContain("/api/metrics/host?range=6h"))
    expect(paths()).toHaveLength(2)
  })

  it("puts the site's domain in the path, escaped", async () => {
    vi.mocked(api.request).mockResolvedValue(siteHistory(2))
    const client = new QueryClient()
    function SiteProbe() {
      useSiteHistory("a b.test", "24h")
      return null
    }
    render(
      <QueryClientProvider client={client}>
        <SiteProbe />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(paths()).toEqual(["/api/metrics/sites/a%20b.test?range=24h"]))
  })
})

describe("classifyHistory", () => {
  it("reads 409 as recording off and 503 as no database", () => {
    expect(classifyHistory({ isPending: false, error: new ApiError(409, "conflict", "off") })).toEqual({
      kind: "disabled",
      detail: "off",
    })
    expect(classifyHistory({ isPending: false, error: new ApiError(503, "unavailable", "no db") })).toEqual({
      kind: "unavailable",
      detail: "no db",
    })
    expect(classifyHistory({ isPending: false, error: new ApiError(500, "internal", "boom") }).kind).toBe("error")
    expect(classifyHistory({ isPending: false, error: new Error("net") }).kind).toBe("error")
    expect(classifyHistory({ isPending: true, error: null }).kind).toBe("loading")
    expect(classifyHistory({ isPending: false, error: null }).kind).toBe("ready")
  })

  it("knows a range with nothing recorded", () => {
    expect(hasNoData([null, null], [])).toBe(true)
    expect(hasNoData([null, 0])).toBe(false)
  })
})

describe("percentile wording", () => {
  it("says the numbers are estimates good to about 20%", () => {
    expect(PERCENTILE_NOTE).toContain("estimates from fixed histogram bins")
    expect(PERCENTILE_NOTE).toContain("about 20%")
    expect(percentileTooltip("p95")).toBe(
      "p95: 95% of requests were faster than this. p50 and p95 are estimates from fixed histogram bins, good to about 20%.",
    )
    expect(percentileTooltip("p50", "upstream responses")).toContain("half of upstream responses")
  })
})
