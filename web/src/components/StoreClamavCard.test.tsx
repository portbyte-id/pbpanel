import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { StoreClamavCard } from "@/components/StoreClamavCard"
import { api, ApiError } from "@/lib/api"
import type { ClamavAvailability } from "@/lib/clamav"

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children: React.ReactNode; to: string; className?: string }) => (
    <a href={String(to)} className={className}>
      {children}
    </a>
  ),
}))

vi.mock("@/components/JobsDrawer", () => ({
  JobsDrawer: ({ jobId }: { jobId: string | null }) =>
    jobId === null ? null : <div data-testid="job-drawer">{jobId}</div>,
}))

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return { ...actual, api: { clamav: vi.fn(), installClamav: vi.fn(), uninstallClamav: vi.fn() } }
})

const availability = (over: Partial<ClamavAvailability> = {}): ClamavAvailability => ({
  installed: false,
  version: "",
  signature_version: "",
  signature_date: "",
  daemon_active: false,
  updater_active: false,
  scanning: false,
  reason: "ClamAV is not installed",
  packages: ["clamav", "clamav-daemon", "clamav-freshclam"],
  ...over,
})

const ready = availability({
  installed: true,
  version: "1.4.3",
  signature_version: "27800",
  signature_date: "Tue Nov 25 08:00:00 2026",
  daemon_active: true,
  updater_active: true,
  reason: "",
})

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <StoreClamavCard />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.mocked(api.clamav).mockReset()
  vi.mocked(api.installClamav).mockReset()
  vi.mocked(api.uninstallClamav).mockReset()
})

describe("StoreClamavCard", () => {
  it("offers an install when nothing is there, and opens the job it starts", async () => {
    vi.mocked(api.clamav).mockResolvedValue(availability())
    vi.mocked(api.installClamav).mockResolvedValue({
      ...availability(),
      job: { id: "job-7", kind: "clamav.install", state: "running" },
    })
    renderCard()

    expect(await screen.findByRole("button", { name: /^install clamav/i })).toBeInTheDocument()
    expect(screen.getByText(/clamav, clamav-daemon, clamav-freshclam/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /uninstall/i })).not.toBeInTheDocument()

    await userEvent.click(screen.getByRole("button", { name: /^install clamav/i }))
    await waitFor(() => expect(api.installClamav).toHaveBeenCalledTimes(1))
    expect(await screen.findByTestId("job-drawer")).toHaveTextContent("job-7")
  })

  it("shows the version and signatures, and offers an uninstall, when ready", async () => {
    vi.mocked(api.clamav).mockResolvedValue(ready)
    vi.mocked(api.uninstallClamav).mockResolvedValue({
      ...ready,
      job: { id: "job-8", kind: "clamav.uninstall", state: "running" },
    })
    renderCard()

    expect(await screen.findByText("1.4.3")).toBeInTheDocument()
    expect(screen.getByText(/27800/)).toBeInTheDocument()
    expect(screen.getByText("ready")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /^install clamav/i })).not.toBeInTheDocument()

    await userEvent.click(screen.getByRole("button", { name: /uninstall clamav/i }))
    await waitFor(() => expect(api.uninstallClamav).toHaveBeenCalledTimes(1))
    expect(await screen.findByTestId("job-drawer")).toHaveTextContent("job-8")
  })

  it("does not let the card uninstall while a scan is running", async () => {
    vi.mocked(api.clamav).mockResolvedValue({ ...ready, scanning: true })
    renderCard()
    expect(await screen.findByRole("button", { name: /uninstall clamav/i })).toBeDisabled()
  })

  it("says so when the host has no signatures yet", async () => {
    vi.mocked(api.clamav).mockResolvedValue(
      availability({ installed: true, version: "1.4.3", reason: "no virus signatures are loaded yet" }),
    )
    renderCard()
    expect(await screen.findByText("no signatures")).toBeInTheDocument()
    expect(screen.getByRole("status")).toHaveTextContent("no virus signatures are loaded yet")
  })

  it("explains a 503 instead of retrying", async () => {
    vi.mocked(api.clamav).mockRejectedValue(new ApiError(503, "unavailable", "ClamAV is not available"))
    renderCard()
    expect(await screen.findByText(/not available on this host/i)).toBeInTheDocument()
    expect(api.clamav).toHaveBeenCalledTimes(1)
  })

  it("shows the server's sentence when the install cannot start", async () => {
    vi.mocked(api.clamav).mockResolvedValue(availability())
    vi.mocked(api.installClamav).mockRejectedValue(new ApiError(500, "internal", "the job queue is full"))
    renderCard()
    await userEvent.click(await screen.findByRole("button", { name: /^install clamav/i }))
    expect(await screen.findByRole("alert")).toHaveTextContent("the job queue is full")
  })
})
