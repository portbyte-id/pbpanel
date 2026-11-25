import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { StoreAppsecCard } from "@/components/StoreAppsecCard"
import { api, ApiError } from "@/lib/api"
import type { AppsecAvailability } from "@/lib/appsec"

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children: React.ReactNode; to: string; className?: string }) => (
    <a href={String(to)} className={className}>
      {children}
    </a>
  ),
}))

// The job drawer opens a stream when it gets an id; this card's tests are about
// the card, so the drawer is stubbed to a marker that carries the id it was given.
vi.mock("@/components/JobsDrawer", () => ({
  JobsDrawer: ({ jobId }: { jobId: string | null }) =>
    jobId === null ? null : <div data-testid="job-drawer">{jobId}</div>,
}))

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return { ...actual, api: { appsec: vi.fn(), installAppsec: vi.fn(), uninstallAppsec: vi.fn() } }
})

/**
 * The tracking statement, as the API sends it. This is the test's own string on
 * purpose: the assertions are that whatever the API holds reaches the screen
 * unchanged, so a copy that matched the server by luck would still be a bad
 * test — and a paraphrase would fail.
 */
const API_WARNING =
  "The open-appsec agent keeps a continuous TLS connection to Check Point's cloud " +
  "(inext-agents.cloud.ngen.checkpoint.com). There is no supported way to turn that connection off: " +
  "--offline_mode does not stop it, and blocking it degrades enforcement, because the agent stops " +
  "inspecting while the connection is unavailable. Running this engine can subject this host and its " +
  "traffic to Check Point's tracking. Install the agent only if you accept that."

const ACK = "open-appsec-cloud-connection-accepted"

const availability = (over: Partial<AppsecAvailability> = {}): AppsecAvailability => ({
  available: false,
  reason: "the open-appsec agent is not installed; install it from the Store",
  installed: false,
  agent_active: false,
  agent_version: "",
  policy_load: "",
  pinned_version: "1.1.37",
  warning: API_WARNING,
  acknowledgement: ACK,
  ...over,
})

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <StoreAppsecCard />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.mocked(api.appsec).mockReset()
  vi.mocked(api.installAppsec).mockReset()
  vi.mocked(api.uninstallAppsec).mockReset()
})

describe("StoreAppsecCard", () => {
  it("shows the tracking warning verbatim before anything is installed", async () => {
    vi.mocked(api.appsec).mockResolvedValue(availability())
    renderCard()

    const mark = await screen.findByRole("note", { name: "open-appsec tracking warning" })
    // Exactly the API string: no truncation, no ellipsis, no rewording.
    expect(mark.textContent).toBe(API_WARNING)
    expect(mark).toHaveAttribute("data-verbatim", "open-appsec-warning")
  })

  it("will not start an install until the warning is acknowledged, then sends the server's own value", async () => {
    const user = userEvent.setup()
    vi.mocked(api.appsec).mockResolvedValue(availability())
    vi.mocked(api.installAppsec).mockResolvedValue({
      ...availability({ installed: true }),
      job: { id: "j1", kind: "appsec.install", state: "running" },
    })
    renderCard()

    const install = await screen.findByRole("button", { name: /Install open-appsec/ })
    expect(install).toBeDisabled()

    // A click on a disabled button does nothing, which is the point.
    await user.click(install)
    expect(api.installAppsec).not.toHaveBeenCalled()

    await user.click(screen.getByRole("checkbox", { name: /I have read the statement above/ }))
    expect(install).toBeEnabled()
    await user.click(install)

    await waitFor(() => expect(api.installAppsec).toHaveBeenCalledTimes(1))
    // The acknowledgement is the API's value, not one the client made up.
    expect(vi.mocked(api.installAppsec).mock.calls[0][0]).toBe(ACK)
  })

  it("shows the agent's version, unit state and policy verdict when it is installed", async () => {
    vi.mocked(api.appsec).mockResolvedValue(
      availability({
        available: true,
        reason: "",
        installed: true,
        agent_active: true,
        agent_version: "1.1.37-open-source",
        policy_load: "Success",
      }),
    )
    renderCard()

    expect(await screen.findByText("1.1.37-open-source")).toBeInTheDocument()
    expect(screen.getByText("running")).toBeInTheDocument()
    expect(screen.getByText("Success")).toBeInTheDocument()
    expect(screen.getByText("ready")).toBeInTheDocument()
    // An installed agent is upgraded by a panel release, not re-installed here.
    expect(screen.queryByRole("button", { name: /Install open-appsec/ })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Uninstall the agent/ })).toBeInTheDocument()
  })

  it("says plainly when the agent is installed but stopped", async () => {
    vi.mocked(api.appsec).mockResolvedValue(
      availability({
        available: false,
        reason: "the open-appsec agent is inactive; start nano_agent.service",
        installed: true,
        agent_active: false,
        agent_version: "1.1.37-open-source",
        policy_load: "Success",
      }),
    )
    renderCard()
    const card = await screen.findByRole("region", { name: "open-appsec engine" })
    // The badge says the state; the table row says the unit's own word for it.
    expect(within(card).getAllByText("stopped")).toHaveLength(2)
    expect(within(card).getByText(/its unit is not running, so it inspects nothing/)).toBeInTheDocument()
  })

  it("keeps the warning on the card once the agent is installed", async () => {
    vi.mocked(api.appsec).mockResolvedValue(
      availability({ available: true, reason: "", installed: true, agent_active: true, agent_version: "1.1.37" }),
    )
    renderCard()
    const mark = await screen.findByRole("note", { name: "open-appsec tracking warning" })
    expect(mark.textContent).toBe(API_WARNING)
  })

  it("starts the uninstall as a job and opens its output", async () => {
    const user = userEvent.setup()
    vi.mocked(api.appsec).mockResolvedValue(
      availability({ available: true, reason: "", installed: true, agent_active: true, agent_version: "1.1.37" }),
    )
    vi.mocked(api.uninstallAppsec).mockResolvedValue({
      ...availability(),
      job: { id: "j2", kind: "appsec.uninstall", state: "running" },
    })
    renderCard()

    await user.click(await screen.findByRole("button", { name: /Uninstall the agent/ }))
    await waitFor(() => expect(api.uninstallAppsec).toHaveBeenCalledTimes(1))
    expect(await screen.findByTestId("job-drawer")).toHaveTextContent("j2")
  })

  it("shows the server's refusal when a site still runs the engine", async () => {
    const user = userEvent.setup()
    vi.mocked(api.appsec).mockResolvedValue(
      availability({ available: true, reason: "", installed: true, agent_active: true, agent_version: "1.1.37" }),
    )
    vi.mocked(api.uninstallAppsec).mockRejectedValue(
      new ApiError(202, "job_failed", "a site still uses the open-appsec engine; switch those sites first"),
    )
    renderCard()

    await user.click(await screen.findByRole("button", { name: /Uninstall the agent/ }))
    expect(await screen.findByRole("alert")).toHaveTextContent(/a site still uses the open-appsec engine/)
  })

  it("says the engine is not part of this build on a 503, rather than offering an install", async () => {
    vi.mocked(api.appsec).mockRejectedValue(
      new ApiError(503, "unavailable", "open-appsec is not available on this host"),
    )
    renderCard()
    expect(await screen.findByText(/This panel build has no open-appsec package/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Install open-appsec/ })).not.toBeInTheDocument()
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })

  it("puts the warning inside the card's own region, so it is read with the install button", async () => {
    vi.mocked(api.appsec).mockResolvedValue(availability())
    renderCard()
    const card = await screen.findByRole("region", { name: "open-appsec engine" })
    const mark = within(card).getByRole("note", { name: "open-appsec tracking warning" })
    expect(mark.textContent).toBe(API_WARNING)
    expect(within(card).getByRole("button", { name: /Install open-appsec/ })).toBeDisabled()
  })
})
