import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { api, type ImageBuild, type NginxImage } from "@/lib/api"
import { BuildCard } from "@/pages/NginxPage"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      nginxImage: vi.fn(),
      updateNginxImage: vi.fn(),
      activateNginxImageBuild: vi.fn(),
      setNginxVersion: vi.fn(),
    },
  }
})

const build = (over: Partial<ImageBuild> = {}): ImageBuild => ({
  id: "20261117175601-a1b2c3",
  kind: "nginx",
  version: "1.29",
  tag: "panel/nginx:1.29",
  image_id: "sha256:abc",
  extensions: [],
  state: "ready",
  output: "",
  created_at: "2026-11-17T17:56:01Z",
  finished_at: "2026-11-17T17:57:00Z",
  current: false,
  previous: false,
  ...over,
})

const image = (over: Partial<NginxImage> = {}): NginxImage => ({
  base: "nginx:1.29",
  repo: "panel/nginx:1.29",
  version: "1.29",
  versions: ["1.30", "1.29", "1.28"],
  stopped: false,
  present: true,
  busy: false,
  current: build({ id: "current-build", current: true }),
  previous: null,
  builds: [],
  ...over,
})

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <BuildCard />
    </QueryClientProvider>,
  )
}

describe("nginx build card", () => {
  beforeEach(() => {
    vi.mocked(api.nginxImage).mockReset()
    vi.mocked(api.updateNginxImage).mockReset()
    vi.mocked(api.activateNginxImageBuild).mockReset()
    // A started job opens its output stream; jsdom has no EventSource.
    class FakeEventSource {
      readyState = 0
      addEventListener() {}
      close() {}
    }
    vi.stubGlobal("EventSource", FakeEventSource)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("switches version only after another one is picked", async () => {
    const user = userEvent.setup()
    vi.mocked(api.nginxImage).mockResolvedValue(image())
    vi.mocked(api.setNginxVersion).mockResolvedValue({ id: "job-v", kind: "nginx.version", state: "running" })
    renderCard()

    const button = await screen.findByRole("button", { name: "Switch version" })
    expect(button).toBeDisabled()
    await user.selectOptions(screen.getByLabelText("nginx version"), "1.28")
    expect(button).toBeEnabled()
    await user.click(button)
    await waitFor(() => expect(api.setNginxVersion).toHaveBeenCalledWith("1.28"))
  })

  it("disables Update image while a build is running", async () => {
    vi.mocked(api.nginxImage).mockResolvedValue(image({ busy: true }))
    renderCard()

    expect(await screen.findByRole("button", { name: "Rebuild nginx" })).toBeDisabled()
    expect(screen.getByText("A build is running.")).toBeInTheDocument()
  })

  it("enables Rebuild nginx when idle and opens the output of the job it starts", async () => {
    const user = userEvent.setup()
    vi.mocked(api.nginxImage).mockResolvedValue(image())
    vi.mocked(api.updateNginxImage).mockResolvedValue({ id: "job-1", kind: "nginx.image", state: "running" })
    renderCard()

    await user.click(await screen.findByRole("button", { name: "Rebuild nginx" }))
    await waitFor(() => expect(api.updateNginxImage).toHaveBeenCalledTimes(1))
    expect(await screen.findByText("job-1")).toBeInTheDocument()
  })

  it("shows Roll back only when a ready previous build exists, and rolls back to it", async () => {
    const user = userEvent.setup()
    vi.mocked(api.nginxImage).mockResolvedValueOnce(image())
    const first = renderCard()
    await screen.findByRole("button", { name: "Rebuild nginx" })
    expect(screen.queryByRole("button", { name: "Roll back" })).toBeNull()
    first.unmount()

    const previous = build({ id: "previous-build", previous: true })
    vi.mocked(api.nginxImage).mockResolvedValue(image({ previous, builds: [previous] }))
    vi.mocked(api.activateNginxImageBuild).mockResolvedValue({ id: "job-2", kind: "nginx.image", state: "running" })
    renderCard()

    await user.click(await screen.findByRole("button", { name: "Roll back" }))
    await waitFor(() => expect(api.activateNginxImageBuild).toHaveBeenCalledWith("previous-build"))
  })

  it("hides Roll back when the previous build never became ready", async () => {
    vi.mocked(api.nginxImage).mockResolvedValue(
      image({ previous: build({ id: "failed-one", state: "failed", previous: true }) }),
    )
    renderCard()

    await screen.findByRole("button", { name: "Rebuild nginx" })
    expect(screen.queryByRole("button", { name: "Roll back" })).toBeNull()
  })

  it("says so when there is no history", async () => {
    vi.mocked(api.nginxImage).mockResolvedValue(image({ current: null, builds: [] }))
    renderCard()

    expect(await screen.findByText("No builds yet.")).toBeInTheDocument()
    expect(screen.getByText("No build yet.")).toBeInTheDocument()
  })

  it("lists nginx builds without an extension column, which only PHP builds have", async () => {
    const current = build({ id: "current-build", current: true })
    vi.mocked(api.nginxImage).mockResolvedValue(image({ current, builds: [current] }))
    renderCard()

    const rows = await screen.findAllByTestId("build-row")
    expect(rows).toHaveLength(1)
    expect(screen.queryByText("no extra extensions")).toBeNull()
    expect(screen.queryByText("Repository")).toBeNull()
    expect(screen.getByText("Built from")).toBeInTheDocument()
  })

  it("names the versions without a flavour suffix", async () => {
    vi.mocked(api.nginxImage).mockResolvedValue(image())
    renderCard()

    const select = await screen.findByLabelText("nginx version")
    expect(select).toHaveDisplayValue("1.29 (running)")
    expect(screen.getByRole("option", { name: "1.30" })).toBeInTheDocument()
  })
})
