import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { FirewallPage } from "@/pages/FirewallPage"
import {
  api,
  ApiError,
  type AllowlistEntry,
  type BlocklistEntry,
  type FirewallRule,
  type FirewallState,
} from "@/lib/api"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      firewall: vi.fn(),
      firewallRules: vi.fn(),
      firewallBlocklist: vi.fn(),
      firewallAllowlist: vi.fn(),
      addAllowlistEntry: vi.fn(),
      removeAllowlistEntry: vi.fn(),
      createFirewallRule: vi.fn(),
      updateFirewallRule: vi.fn(),
      deleteFirewallRule: vi.fn(),
      addBlocklistEntry: vi.fn(),
      removeBlocklistEntry: vi.fn(),
      enableFirewall: vi.fn(),
      applyFirewall: vi.fn(),
      confirmFirewall: vi.fn(),
      revertFirewall: vi.fn(),
      disableFirewall: vi.fn(),
    },
  }
})

const baseState = (over: Partial<FirewallState> = {}): FirewallState => ({
  enabled: true,
  ipv6: true,
  dirty: false,
  sshPorts: [22],
  panelPort: 8443,
  pending: null,
  dockerPorts: [],
  builtins: [{ label: "SSH", detail: "Port 22 is always open" }],
  ...over,
})

const rule = (over: Partial<FirewallRule> = {}): FirewallRule => ({
  id: "r1",
  portStart: 443,
  portEnd: 443,
  protocol: "tcp",
  enabled: true,
  ...over,
})

function setup(
  state: FirewallState,
  rules: FirewallRule[] = [],
  blocklist: BlocklistEntry[] = [],
  allowlist: AllowlistEntry[] = [],
) {
  vi.mocked(api.firewall).mockResolvedValue(state)
  vi.mocked(api.firewallRules).mockResolvedValue(rules)
  vi.mocked(api.firewallBlocklist).mockResolvedValue(blocklist)
  vi.mocked(api.firewallAllowlist).mockResolvedValue(allowlist)
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <FirewallPage />
    </QueryClientProvider>,
  )
  return client
}

const inSeconds = (s: number) => new Date(Date.now() + s * 1000).toISOString()

describe("FirewallPage", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) (fn as ReturnType<typeof vi.fn>).mockReset()
  })

  it("shows disabled status and states what enabling does", async () => {
    setup(baseState({ enabled: false, sshPorts: [22, 2222] }))
    const user = userEvent.setup()

    expect(await screen.findByText("Disabled")).toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Enable firewall" }))

    const dialog = await screen.findByRole("dialog")
    expect(dialog).toHaveTextContent(/ufw will be stopped and masked/)
    expect(dialog).toHaveTextContent(/60 second/)
    expect(dialog).toHaveTextContent("22, 2222")
    expect(dialog).toHaveTextContent("8443")
    expect(api.enableFirewall).not.toHaveBeenCalled()

    const started = { token: "t1", startedAt: new Date().toISOString(), deadline: inSeconds(60) }
    vi.mocked(api.enableFirewall).mockImplementation(async () => {
      // The refetch after the call must see what the real server would report.
      vi.mocked(api.firewall).mockResolvedValue(baseState({ pending: started }))
      return started
    })
    await user.click(within(dialog).getByRole("button", { name: "Enable firewall" }))
    expect(await screen.findByRole("button", { name: "Keep" })).toBeInTheDocument()
  })

  it("renders a pending apply with a countdown, Keep and Revert now", async () => {
    setup(
      baseState({
        dirty: true,
        pending: { token: "tok", startedAt: inSeconds(-10), deadline: inSeconds(45) },
      }),
    )
    const user = userEvent.setup()

    const countdown = await screen.findByTestId("countdown")
    expect(Number.parseInt(countdown.textContent ?? "", 10)).toBeGreaterThan(40)
    expect(screen.getByRole("button", { name: "Revert now" })).toBeInTheDocument()
    // The apply button gives way to the countdown.
    expect(screen.queryByRole("button", { name: "Apply changes" })).not.toBeInTheDocument()

    vi.mocked(api.confirmFirewall).mockResolvedValue(undefined)
    await user.click(screen.getByRole("button", { name: "Keep" }))
    await waitFor(() => expect(api.confirmFirewall).toHaveBeenCalledWith("tok"))
  })

  it("calls revert from Revert now", async () => {
    setup(
      baseState({ pending: { token: "tok", startedAt: inSeconds(-1), deadline: inSeconds(30) } }),
    )
    vi.mocked(api.revertFirewall).mockResolvedValue(undefined)

    await userEvent.setup().click(await screen.findByRole("button", { name: "Revert now" }))
    await waitFor(() => expect(api.revertFirewall).toHaveBeenCalled())
  })

  it("says the rules are being restored and refetches once the deadline passed", async () => {
    setup(
      baseState({ pending: { token: "tok", startedAt: inSeconds(-70), deadline: inSeconds(-5) } }),
    )

    expect(await screen.findByText(/previous rules are being restored/i)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Keep" })).not.toBeInTheDocument()
    // First load plus the refetch the expiry triggers.
    await waitFor(() =>
      expect(vi.mocked(api.firewall).mock.calls.length).toBeGreaterThanOrEqual(2),
    )
  })

  it("shows Apply changes when dirty and starts the countdown from the answer", async () => {
    setup(baseState({ dirty: true }))
    expect(await screen.findByText("Unapplied changes")).toBeInTheDocument()

    const started = { token: "t2", startedAt: new Date().toISOString(), deadline: inSeconds(60) }
    vi.mocked(api.applyFirewall).mockImplementation(async () => {
      vi.mocked(api.firewall).mockResolvedValue(baseState({ dirty: true, pending: started }))
      return started
    })
    await userEvent.setup().click(screen.getByRole("button", { name: "Apply changes" }))
    expect(await screen.findByTestId("countdown")).toBeInTheDocument()
  })

  it("hides the apply bar when nothing differs", async () => {
    setup(baseState())
    await screen.findByText("Enabled")
    expect(screen.queryByRole("button", { name: "Apply changes" })).not.toBeInTheDocument()
  })

  it("warns about Docker published ports only when there are some", async () => {
    setup(baseState({ dockerPorts: [{ container: "redis", port: 6379, protocol: "tcp" }] }))
    expect(await screen.findByText(/Docker published ports bypass these rules/)).toBeInTheDocument()
    expect(screen.getByText("redis: 6379/tcp")).toBeInTheDocument()
  })

  it("has no Docker warning without published ports", async () => {
    setup(baseState())
    await screen.findByText("Enabled")
    expect(screen.queryByText(/bypass these rules/)).not.toBeInTheDocument()
  })

  it("lists built-in rows read-only and the operator rules with actions", async () => {
    setup(baseState(), [rule({ comment: "web", source: "10.0.0.0/8" })])

    const builtin = await screen.findByTestId("builtin-row")
    expect(within(builtin).getByText("SSH")).toBeInTheDocument()
    expect(within(builtin).queryByRole("button")).not.toBeInTheDocument()

    expect(await screen.findByText("10.0.0.0/8")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Edit rule for port 443" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Delete rule for port 443" })).toBeInTheDocument()
  })

  it("adds a rule from a port range", async () => {
    setup(baseState())
    vi.mocked(api.createFirewallRule).mockResolvedValue(rule())
    const user = userEvent.setup()

    await user.click(await screen.findByRole("button", { name: "Add rule" }))
    const dialog = await screen.findByRole("dialog")
    await user.type(within(dialog).getByLabelText("Port or range"), "8000-8100")
    await user.selectOptions(within(dialog).getByLabelText("Protocol"), "udp")
    await user.type(within(dialog).getByLabelText("Allowed from"), "10.0.0.0/8")
    await user.click(within(dialog).getByRole("button", { name: "Add rule" }))

    await waitFor(() =>
      expect(api.createFirewallRule).toHaveBeenCalledWith({
        portStart: 8000,
        portEnd: 8100,
        protocol: "udp",
        source: "10.0.0.0/8",
        enabled: true,
      }),
    )
  })

  it("shows a server validation error under the field", async () => {
    setup(baseState())
    vi.mocked(api.createFirewallRule).mockRejectedValue(
      new ApiError(400, "invalid_rule", "source is not a valid CIDR", { field: "source" }),
    )
    const user = userEvent.setup()

    await user.click(await screen.findByRole("button", { name: "Add rule" }))
    const dialog = await screen.findByRole("dialog")
    await user.type(within(dialog).getByLabelText("Port or range"), "80")
    await user.type(within(dialog).getByLabelText("Allowed from"), "nope")
    await user.click(within(dialog).getByRole("button", { name: "Add rule" }))

    expect(await within(dialog).findByText("source is not a valid CIDR")).toBeInTheDocument()
    expect(within(dialog).getByLabelText("Allowed from")).toHaveAttribute("aria-invalid", "true")
  })

  it("deletes a rule only after confirmation", async () => {
    setup(baseState(), [rule()])
    vi.mocked(api.deleteFirewallRule).mockResolvedValue(undefined)
    const user = userEvent.setup()

    await user.click(await screen.findByRole("button", { name: "Delete rule for port 443" }))
    expect(api.deleteFirewallRule).not.toHaveBeenCalled()
    const dialog = await screen.findByRole("dialog")
    await user.click(within(dialog).getByRole("button", { name: "Delete rule" }))
    await waitFor(() => expect(api.deleteFirewallRule).toHaveBeenCalledWith("r1"))
  })

  it("adds and removes blocklist entries", async () => {
    setup(baseState(), [], [{ id: "b1", addr: "198.51.100.7", comment: "scanner" }])
    vi.mocked(api.addBlocklistEntry).mockResolvedValue({ id: "b2", addr: "203.0.113.9" })
    vi.mocked(api.removeBlocklistEntry).mockResolvedValue(undefined)
    const user = userEvent.setup()

    await user.click(await screen.findByRole("tab", { name: "Blocklist" }))
    expect(await screen.findByText("198.51.100.7")).toBeInTheDocument()
    await user.type(screen.getByLabelText("Address or CIDR"), "203.0.113.9")
    await user.click(screen.getByRole("button", { name: "Block address" }))
    await waitFor(() =>
      expect(api.addBlocklistEntry).toHaveBeenCalledWith({ addr: "203.0.113.9" }),
    )

    await user.click(screen.getByRole("button", { name: "Unblock 198.51.100.7" }))
    await waitFor(() => expect(api.removeBlocklistEntry).toHaveBeenCalledWith("b1"))
  })

  it("adds and removes allow list entries", async () => {
    setup(baseState(), [], [], [{ id: "a1", addr: "198.51.100.9/32", comment: "office" }])
    vi.mocked(api.addAllowlistEntry).mockResolvedValue({ id: "a2", addr: "203.0.113.20" })
    vi.mocked(api.removeAllowlistEntry).mockResolvedValue(undefined)
    const user = userEvent.setup()

    await user.click(await screen.findByRole("tab", { name: "Allow list" }))
    expect(await screen.findByText("198.51.100.9/32")).toBeInTheDocument()
    await user.type(screen.getByLabelText("Address or CIDR"), "203.0.113.20")
    await user.click(screen.getByRole("button", { name: "Allow address" }))
    await waitFor(() =>
      expect(api.addAllowlistEntry).toHaveBeenCalledWith({ addr: "203.0.113.20" }),
    )

    await user.click(screen.getByRole("button", { name: "Remove 198.51.100.9/32 from the allow list" }))
    await waitFor(() => expect(api.removeAllowlistEntry).toHaveBeenCalledWith("a1"))
  })

  it("shows an empty allow list", async () => {
    setup(baseState())
    const user = userEvent.setup()
    await user.click(await screen.findByRole("tab", { name: "Allow list" }))
    expect(await screen.findByText("No allowed addresses.")).toBeInTheDocument()
  })

  it("asks before disabling", async () => {
    setup(baseState())
    vi.mocked(api.disableFirewall).mockResolvedValue(undefined)
    const user = userEvent.setup()

    await user.click(await screen.findByRole("button", { name: "Disable firewall" }))
    expect(api.disableFirewall).not.toHaveBeenCalled()
    const dialog = await screen.findByRole("dialog")
    await user.click(within(dialog).getByRole("button", { name: "Disable firewall" }))
    await waitFor(() => expect(api.disableFirewall).toHaveBeenCalled())
  })

  it("shows the server sentence when keep arrives too late", async () => {
    setup(
      baseState({ pending: { token: "tok", startedAt: inSeconds(-1), deadline: inSeconds(30) } }),
    )
    vi.mocked(api.confirmFirewall).mockRejectedValue(
      new ApiError(410, "expired", "The window to keep this change has closed."),
    )

    await userEvent.setup().click(await screen.findByRole("button", { name: "Keep" }))
    expect(
      await screen.findByText("The window to keep this change has closed."),
    ).toBeInTheDocument()
  })
})
