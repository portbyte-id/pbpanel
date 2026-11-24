import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { api, type SiteDetail } from "@/lib/api"
import { SiteDetailBody } from "@/pages/SiteDetailPage"
import { streamOf } from "@/test/helpers"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return { ...actual, api: { site: vi.fn(), openStream: vi.fn() } }
})

function detail(overrides: Partial<SiteDetail> = {}): SiteDetail {
  const inc = (name: string) => `/www/panel/nginx/includes/app.test/${name}.conf`
  return {
    domain: "app.test",
    aliases: [],
    kind: "proxy",
    proxy_target: "http://127.0.0.1:3000",
    php_version: null,
    created_at: "2026-11-17T12:00:00Z",
    site_file: "/www/panel/nginx/sites/app.test.conf",
    site_file_missing: false,
    features: [
      { feature: "listen", include: inc("listen"), attached: true },
      { feature: "names", include: inc("names"), attached: true },
      { feature: "acme", include: inc("acme"), attached: true },
      { feature: "proxy", include: inc("proxy"), attached: true },
    ],
    ...overrides,
  }
}

function setup(site: SiteDetail) {
  vi.mocked(api.site).mockResolvedValue(site)
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <SiteDetailBody domain={site.domain} onGone={() => {}} />
    </QueryClientProvider>,
  )
}

const pathOf = (call: number) => String(vi.mocked(api.openStream).mock.calls[call]?.[0])

describe("site Logs section", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) (fn as ReturnType<typeof vi.fn>).mockReset()
  })

  it("offers the access and error logs for a proxied site and tails the access log first", async () => {
    vi.mocked(api.openStream).mockResolvedValue(streamOf(['event: line\ndata: {"line":"GET / 200"}\n\n']))
    setup(detail())
    const user = userEvent.setup()

    await user.click(await screen.findByRole("button", { name: "Logs" }))
    const tabs = within(screen.getByRole("tablist", { name: "Log files" }))
    expect(tabs.getAllByRole("tab").map((t) => t.textContent)).toEqual(["Access log", "Error log"])

    await waitFor(() => expect(pathOf(0)).toContain("/api/logs/site%3Aapp.test%3Aaccess/tail"))
    expect(await screen.findByText("GET / 200")).toBeInTheDocument()
  })

  it("switches to the error log", async () => {
    vi.mocked(api.openStream).mockResolvedValue(streamOf([]))
    setup(detail())
    const user = userEvent.setup()

    await user.click(await screen.findByRole("button", { name: "Logs" }))
    await user.click(screen.getByRole("tab", { name: "Error log" }))

    await waitFor(() => expect(pathOf(1)).toContain("/api/logs/site%3Aapp.test%3Aerror/tail"))
    expect(screen.getByLabelText("Log of app.test error log")).toBeInTheDocument()
  })

  it("adds the PHP error log for a PHP site", async () => {
    vi.mocked(api.openStream).mockResolvedValue(streamOf([]))
    setup(detail({ kind: "php", php_version: "8.4", proxy_target: undefined, root: "/www/sites/app.test" }))
    const user = userEvent.setup()

    await user.click(await screen.findByRole("button", { name: "Logs" }))
    const tabs = within(screen.getByRole("tablist", { name: "Log files" }))
    expect(tabs.getAllByRole("tab").map((t) => t.textContent)).toEqual([
      "Access log",
      "Error log",
      "PHP error log",
    ])

    await user.click(tabs.getByRole("tab", { name: "PHP error log" }))
    await waitFor(() => expect(pathOf(1)).toContain("/api/logs/site%3Aapp.test%3Aphp/tail"))
  })
})
