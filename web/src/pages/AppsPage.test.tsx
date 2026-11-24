import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { api, type PanelApp, type Site, type SiteDetail } from "@/lib/api"
import { AppsTable } from "@/pages/AppsPage"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return { ...actual, api: { site: vi.fn(), runtimes: vi.fn(), siteIncludes: vi.fn(), openStream: vi.fn() } }
})

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: React.ReactNode }) => <a href="#site">{children}</a>,
}))

const app: PanelApp = {
  id: 1,
  name: "shop",
  type: "go",
  command: "/www/shop/bin/shop",
  args: [],
  workdir: "",
  env: {},
  user: "www",
  node_version: "",
  autostart: true,
  restart: "always",
  port: 3000,
  created_at: "2026-11-17T12:00:00Z",
  state: "RUNNING",
  pid: 10,
  uptime_seconds: 60,
  exitstatus: 0,
  spawn_error: "",
  proxy_url: "http://127.0.0.1:3000",
}

function site(domain: string): Site {
  return {
    domain,
    aliases: [],
    kind: "proxy",
    proxy_target: "http://127.0.0.1:3000",
    php_version: null,
    created_at: "2026-11-17T12:00:00Z",
  } as Site
}

function detail(s: Site): SiteDetail {
  const inc = (name: string) => `/www/panel/nginx/includes/${s.domain}/${name}.conf`
  return {
    ...s,
    site_file: `/www/panel/nginx/sites/${s.domain}.conf`,
    site_file_missing: false,
    features: [{ feature: "proxy", include: inc("proxy"), attached: true }],
  } as SiteDetail
}

function setup(sites: Site[]) {
  vi.mocked(api.site).mockImplementation(async (d: string) => detail(sites.find((s) => s.domain === d) as Site))
  vi.mocked(api.runtimes).mockResolvedValue({ node: [] } as never)
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <AppsTable type="go" apps={[app]} sites={sites} certs={new Map()} certsPending={false} intro="" empty="" />
    </QueryClientProvider>,
  )
}

describe("the app dialog's domain menu", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) (fn as ReturnType<typeof vi.fn>).mockReset()
  })

  it("puts the domain sections in the app's sidebar, with no Configure button per domain", async () => {
    setup([site("a.example.com"), site("b.example.com")])
    const user = userEvent.setup()

    await user.click(screen.getByRole("button", { name: "Manage" }))
    const nav = within(await screen.findByRole("navigation", { name: "App sections" }))
    expect(nav.getAllByRole("button").map((b) => b.textContent)).toEqual([
      "Settings",
      "Domains",
      "Reverse proxy",
      "Config",
      "SSL",
      "Firewall",
      "Site logs",
      "Reset or delete",
      "App logs",
      "Delete app",
    ])
    expect(screen.queryByRole("button", { name: "Configure" })).not.toBeInTheDocument()
  })

  it("switches the menu between the app's domains in the same dialog", async () => {
    setup([site("a.example.com"), site("b.example.com")])
    const user = userEvent.setup()

    await user.click(screen.getByRole("button", { name: "Manage" }))
    await user.click(await screen.findByRole("button", { name: "Reverse proxy" }))
    await waitFor(() => expect(api.site).toHaveBeenCalledWith("a.example.com"))

    await user.selectOptions(screen.getByLabelText("Domain"), "b.example.com")
    await waitFor(() => expect(api.site).toHaveBeenCalledWith("b.example.com"))
    // Still the Reverse proxy section, now for the second domain.
    expect(screen.getByRole("button", { name: "Reverse proxy" })).toHaveAttribute("aria-current", "page")
  })

  it("offers one Domains section while no domain points at the app", async () => {
    setup([])
    const user = userEvent.setup()

    await user.click(screen.getByRole("button", { name: "Manage" }))
    const nav = within(await screen.findByRole("navigation", { name: "App sections" }))
    expect(nav.getAllByRole("button").map((b) => b.textContent)).toEqual([
      "Settings",
      "Domains",
      "App logs",
      "Delete app",
    ])
  })
})
