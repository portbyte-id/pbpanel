import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  api,
  ApiError,
  type CertView,
  type FirewallState,
  type SiteDetail,
} from "@/lib/api"
import type { ServiceRow } from "@/lib/sysinfo"
import { AlertsPanel, DashboardPage, MetricTiles } from "@/pages/DashboardPage"
import { snap } from "@/test/helpers"

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children: React.ReactNode; to: string; className?: string }) => (
    <a href={String(to)} className={className}>
      {children}
    </a>
  ),
}))

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      sysinfo: vi.fn(),
      services: vi.fn(),
      controlService: vi.fn(),
      tlsCerts: vi.fn(),
      firewall: vi.fn(),
      sites: vi.fn(),
      site: vi.fn(),
      cronJobs: vi.fn(),
      audit: vi.fn(),
    },
  }
})

const services: ServiceRow[] = [
  { name: "panel", kind: "systemd", state: "running", detail: "active", actions: ["start", "restart"] },
  {
    name: "panel-nginx",
    kind: "container",
    state: "running",
    detail: "Up 3 hours",
    actions: ["start", "stop", "restart"],
  },
  { name: "app:mysite", kind: "app", state: "failed", detail: "FATAL", actions: ["start", "stop", "restart"] },
  { name: "docker", kind: "systemd", state: "unknown", detail: "systemctl not reachable", actions: [] },
]

const firewall: FirewallState = {
  enabled: true,
  ipv6: true,
  dirty: false,
  sshPorts: [22],
  panelPort: 8443,
  pending: null,
  dockerPorts: [],
  builtins: [],
}

const cert = (over: Partial<CertView>): CertView => ({
  domain: "a.test",
  source: "http01",
  names: ["a.test"],
  not_before: "",
  not_after: "",
  issuer: "",
  serial: "",
  days_left: 60,
  last_renewal_at: null,
  last_renewal_result: "",
  last_renewal_error: "",
  redirect_http: false,
  provider: "",
  ...over,
})

function setup(over: { certs?: CertView[]; sites?: SiteDetail[]; auditFails?: boolean } = {}) {
  const latest = snap("2026-11-17T10:00:05Z", {
    cpu_percent: 12.5,
    disks: [
      {
        mount: "/",
        device: "/dev/sda1",
        fstype: "ext4",
        total_bytes: 100 * 1024 ** 3,
        used_bytes: 91 * 1024 ** 3,
        free_bytes: 9 * 1024 ** 3,
        used_percent: 91,
      },
    ],
    net: [
      { iface: "lo", rx_bytes_per_sec: 999_999, tx_bytes_per_sec: 999_999, rx_bytes: 0, tx_bytes: 0 },
      { iface: "eth0", rx_bytes_per_sec: 2048, tx_bytes_per_sec: 1024, rx_bytes: 0, tx_bytes: 0 },
    ],
  })
  vi.mocked(api.sysinfo).mockResolvedValue({
    interval_seconds: 5,
    latest,
    history: [snap("2026-11-17T10:00:00Z"), latest],
  })
  vi.mocked(api.services).mockResolvedValue(services)
  vi.mocked(api.tlsCerts).mockResolvedValue(over.certs ?? [])
  vi.mocked(api.firewall).mockResolvedValue(firewall)
  vi.mocked(api.sites).mockResolvedValue((over.sites ?? []).map((s) => ({ ...s })))
  vi.mocked(api.site).mockImplementation(async (d) => {
    const found = (over.sites ?? []).find((s) => s.domain === d)
    if (!found) throw new Error("no such site")
    return found
  })
  vi.mocked(api.cronJobs).mockResolvedValue([])
  if (over.auditFails) {
    vi.mocked(api.audit).mockRejectedValue(new ApiError(500, "internal_error", "audit store down"))
  } else {
    vi.mocked(api.audit).mockResolvedValue({ entries: [], limit: 100 })
  }
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <DashboardPage />
    </QueryClientProvider>,
  )
}

describe("DashboardPage", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) (fn as ReturnType<typeof vi.fn>).mockReset()
  })

  it("shows the metric tiles, leaving loopback out of the network rates", async () => {
    setup()
    expect(await screen.findByTestId("tile-CPU")).toHaveTextContent("13%")
    expect(screen.getByTestId("tile-Memory")).toHaveTextContent("50%")
    expect(screen.getByTestId("tile-Swap")).toHaveTextContent("No swap")
    expect(screen.getByTestId("tile-Uptime")).toHaveTextContent("3d 4h")
    expect(screen.getByTestId("tile-Network")).toHaveTextContent("↓ 2.00 KiB/s")
    expect(screen.getByRole("meter", { name: "Disk use of /" })).toHaveAttribute("aria-valuenow", "91")
    // No EventSource in jsdom: the page says it is polling rather than pretending to be live.
    expect(screen.getByText(/Refreshing every 5 seconds/)).toBeInTheDocument()
    expect(screen.getByRole("img", { name: /CPU use over the last 2 samples/ })).toBeInTheDocument()
  })

  it("honours each row's actions and has no Stop for the panel", async () => {
    setup()
    const panel = await screen.findByText("panel", { selector: "p" })
    const card = panel.closest("li") as HTMLElement
    expect(within(card).getByRole("button", { name: "Restart panel" })).toBeInTheDocument()
    expect(within(card).getByRole("button", { name: "Start panel" })).toBeInTheDocument()
    expect(within(card).queryByRole("button", { name: "Stop panel" })).not.toBeInTheDocument()

    const docker = screen.getByText("docker", { selector: "p" }).closest("li") as HTMLElement
    expect(within(docker).queryAllByRole("button")).toHaveLength(0)
    expect(within(docker).getByText("systemctl not reachable")).toBeInTheDocument()
  })

  it("asks before stopping, then stops, and starts without asking", async () => {
    setup()
    const user = userEvent.setup()
    vi.mocked(api.controlService).mockResolvedValue(undefined)

    await user.click(await screen.findByRole("button", { name: "Stop panel-nginx" }))
    const dialog = await screen.findByRole("dialog")
    expect(dialog).toHaveTextContent("Stop panel-nginx?")
    expect(api.controlService).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole("button", { name: "Stop" }))
    await waitFor(() => expect(api.controlService).toHaveBeenCalledWith("panel-nginx", "stop"))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    await user.click(screen.getByRole("button", { name: "Start app:mysite" }))
    await waitFor(() => expect(api.controlService).toHaveBeenCalledWith("app:mysite", "start"))
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })

  it("warns that restarting the panel drops this page's connection", async () => {
    setup()
    const user = userEvent.setup()
    await user.click(await screen.findByRole("button", { name: "Restart panel" }))
    expect(await screen.findByRole("dialog")).toHaveTextContent(/loses its connection/)
  })

  it("keeps the dialog open and shows the command's output when the action fails", async () => {
    setup()
    const user = userEvent.setup()
    vi.mocked(api.controlService).mockRejectedValue(
      new ApiError(502, "control_failed", "The command failed.", { output: "Failed to stop: unit masked" }),
    )
    await user.click(await screen.findByRole("button", { name: "Restart panel-nginx" }))
    const dialog = await screen.findByRole("dialog")
    await user.click(within(dialog).getByRole("button", { name: "Restart" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "The command failed. Failed to stop: unit masked",
    )
  })

  it("lists alerts from certificates, sites and a failed service, errors first", async () => {
    setup({
      certs: [cert({ domain: "bad.test", last_renewal_result: "failed", last_renewal_error: "dns: zone not found" })],
      sites: [
        {
          domain: "a.test",
          aliases: [],
          kind: "static",
          php_version: null,
          created_at: "",
          site_file: "/x",
          site_file_missing: false,
          features: [{ feature: "tls", include: "tls.conf", attached: false }],
        },
      ],
    })
    expect(await screen.findByText("Renewal failed for bad.test")).toBeInTheDocument()
    expect(screen.getByText("dns: zone not found")).toBeInTheDocument()
    expect(await screen.findByText("a.test: tls detached from the site file")).toBeInTheDocument()
    expect(screen.getByText("app:mysite has failed")).toBeInTheDocument()
    const severities = screen
      .getAllByRole("listitem")
      .filter((li) => li.hasAttribute("data-severity"))
      .map((li) => li.getAttribute("data-severity"))
    expect(severities).toEqual(["error", "error", "warning"])
  })

  it("names a source it could not read instead of reporting all clear", async () => {
    setup({ auditFails: true })
    expect(await screen.findByText(/Could not read: audit log/)).toBeInTheDocument()
    expect(screen.queryByText("Nothing needs attention.")).not.toBeInTheDocument()
    expect(screen.getByText(/Failed background jobs are not listed here/)).toBeInTheDocument()
  })
})

describe("MetricTiles", () => {
  it("says it is measuring until there are two readings", () => {
    const first = snap("2026-11-17T10:00:00Z", { rates: false, cpu_percent: 0 })
    render(<MetricTiles latest={first} history={[first]} />)
    expect(screen.getByTestId("tile-CPU")).toHaveTextContent("Measuring…")
    expect(screen.getByTestId("tile-Network")).toHaveTextContent("Measuring…")
  })

  it("waits for the first sample", () => {
    render(<MetricTiles latest={null} history={[]} />)
    expect(screen.getByText("Waiting for the first sample.")).toBeInTheDocument()
  })

  it("shows swap use when there is swap", () => {
    const s = snap("2026-11-17T10:00:00Z")
    s.memory.swap_total_bytes = 2 * 1024 ** 3
    s.memory.swap_used_bytes = 1024 ** 3
    render(<MetricTiles latest={s} history={[s]} />)
    expect(screen.getByTestId("tile-Swap")).toHaveTextContent("50%")
  })
})

describe("AlertsPanel", () => {
  it("says all clear only when everything was read", () => {
    render(<AlertsPanel alerts={[]} unchecked={[]} checking={false} />)
    expect(screen.getByText("Nothing needs attention.")).toBeInTheDocument()
  })
})
