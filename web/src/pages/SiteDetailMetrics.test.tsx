import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { api, type SiteDetail } from "@/lib/api"
import { SiteDetailBody, sectionFromSearch, siteSectionList } from "@/pages/SiteDetailPage"
import { siteHistory } from "@/test/metricsFixtures"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return { ...actual, api: { site: vi.fn(), request: vi.fn(), siteTLS: vi.fn(), tlsProviders: vi.fn() } }
})

vi.mock("@tanstack/react-router", () => ({
  useSearch: () => ({}),
  useNavigate: () => vi.fn(),
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => <a href={String(to)}>{children}</a>,
}))

const site: SiteDetail = {
  domain: "app.test",
  aliases: [],
  kind: "proxy",
  proxy_target: "http://127.0.0.1:3000",
  php_version: null,
  created_at: "2026-11-17T12:00:00Z",
  site_file: "/www/panel/nginx/sites/app.test.conf",
  site_file_missing: false,
  features: [],
}

function setup(initialSection?: Parameters<typeof SiteDetailBody>[0]["initialSection"]) {
  vi.mocked(api.site).mockResolvedValue(site)
  vi.mocked(api.request).mockResolvedValue(siteHistory())
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <SiteDetailBody domain={site.domain} onGone={() => {}} initialSection={initialSection} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  for (const fn of Object.values(api)) (fn as ReturnType<typeof vi.fn>).mockReset()
})

describe("site Metrics section", () => {
  it("has a Metrics entry in the rail, before Logs", () => {
    expect(siteSectionList(false).map((s) => s.label)).toEqual([
      "Domain manager",
      "Reverse proxy",
      "Config",
      "SSL",
      "Firewall",
      "Metrics",
      "Logs",
      "Reset or delete",
    ])
  })

  it("opens from the menu", async () => {
    setup()
    const user = userEvent.setup()
    await user.click(await screen.findByRole("button", { name: "Metrics" }))
    expect(await screen.findByRole("figure", { name: "Requests by status class" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Metrics" })).toHaveAttribute("aria-current", "page")
  })

  it("opens on the section named by ?section=metrics", async () => {
    setup(sectionFromSearch("metrics"))
    expect(await screen.findByRole("figure", { name: "Requests by status class" })).toBeInTheDocument()
  })

  it("opens on ?section=ssl too, and ignores an unknown section", async () => {
    expect(sectionFromSearch("ssl")).toBe("ssl")
    expect(sectionFromSearch("nope")).toBeUndefined()
    expect(sectionFromSearch(undefined)).toBeUndefined()

    vi.mocked(api.siteTLS).mockResolvedValue(null)
    vi.mocked(api.tlsProviders).mockResolvedValue({ providers: [], kinds: [] })
    setup("ssl")
    expect(await screen.findByRole("button", { name: "SSL" })).toHaveAttribute("aria-current", "page")
  })
})
