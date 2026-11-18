import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { api, ApiError } from "@/lib/api"
import {
  imageLabel,
  portLabel,
  shortId,
  stateTone,
  uniquePorts,
  type DockerContainer,
  type DockerImage,
  type DockerInfo,
  type DockerVolume,
} from "@/lib/docker"
import { DockerPage } from "@/pages/DockerPage"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      dockerInfo: vi.fn(),
      dockerContainers: vi.fn(),
      dockerStats: vi.fn(),
      dockerProcesses: vi.fn(),
      dockerLogs: vi.fn(),
      dockerControl: vi.fn(),
      dockerRemoveContainer: vi.fn(),
      dockerImages: vi.fn(),
      dockerRemoveImage: vi.fn(),
      dockerVolumes: vi.fn(),
      dockerRemoveVolume: vi.fn(),
    },
  }
})

const info: DockerInfo = {
  serverVersion: "29.9.0",
  operatingSystem: "Ubuntu",
  kernelVersion: "6.8",
  storageDriver: "overlay2",
  rootDir: "/var/lib/docker",
  cpus: 4,
  memTotal: 8 * 1024 ** 3,
  containers: 3,
  running: 2,
  paused: 0,
  stopped: 1,
  images: 3,
}

const container = (over: Partial<DockerContainer> = {}): DockerContainer => ({
  id: "aaaa1111bbbb2222cccc3333",
  name: "redis",
  image: "redis:7",
  imageId: "sha256:r",
  command: "redis-server",
  created: "2026-11-17T00:00:00Z",
  state: "running",
  status: "Up 2 hours",
  ports: [],
  labels: {},
  volumes: [],
  managed: false,
  ...over,
})

const image = (over: Partial<DockerImage> = {}): DockerImage => ({
  id: "sha256:abcdef0123456789",
  tags: ["redis:7"],
  digests: [],
  size: 1024 * 1024 * 100,
  created: "2026-11-01T00:00:00Z",
  dangling: false,
  inUseBy: [],
  managed: false,
  ...over,
})

const volume = (over: Partial<DockerVolume> = {}): DockerVolume => ({
  name: "spare",
  driver: "local",
  mountpoint: "/var/lib/docker/volumes/spare/_data",
  scope: "local",
  createdAt: "2026-11-01T00:00:00Z",
  labels: {},
  inUseBy: [],
  ...over,
})

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <DockerPage />
    </QueryClientProvider>,
  )
}

function setup(containers: DockerContainer[] = [container()]) {
  vi.mocked(api.dockerInfo).mockResolvedValue(info)
  vi.mocked(api.dockerContainers).mockResolvedValue(containers)
  vi.mocked(api.dockerImages).mockResolvedValue([image(), image({ id: "sha256:used", tags: ["app:1"], inUseBy: ["app"] })])
  vi.mocked(api.dockerVolumes).mockResolvedValue([volume(), volume({ name: "data", inUseBy: ["app"] })])
  vi.mocked(api.dockerStats).mockResolvedValue([
    { id: "a", name: "light", cpuPercent: 1, memUsage: 1024, memLimit: 2048, memPercent: 50, netRx: 1, netTx: 2, blockRead: 3, blockWrite: 4, pids: 5 },
    { id: "b", name: "busy", cpuPercent: 90, memUsage: 1024, memLimit: 0, memPercent: 0, netRx: 0, netTx: 0, blockRead: 0, blockWrite: 0, pids: 9 },
  ])
  mount()
}

const containerRow = async (name: string) => {
  await screen.findAllByTestId("docker-container-row")
  const el = screen.getAllByTestId("docker-container-row").find((r) => r.getAttribute("data-container") === name)
  if (!el) throw new Error(`no row for ${name}`)
  return within(el)
}

describe("docker helpers", () => {
  it("labels ports and drops the IPv6 twin of a published port", () => {
    expect(portLabel({ privatePort: 80, type: "tcp" })).toBe("80/tcp")
    expect(portLabel({ ip: "0.0.0.0", privatePort: 80, publicPort: 8080, type: "tcp" })).toBe("8080→80/tcp")
    expect(portLabel({ ip: "127.0.0.1", privatePort: 80, publicPort: 8080, type: "tcp" })).toBe("127.0.0.1:8080→80/tcp")
    const both = uniquePorts([
      { ip: "0.0.0.0", privatePort: 80, publicPort: 8080, type: "tcp" },
      { ip: "::", privatePort: 80, publicPort: 8080, type: "tcp" },
    ])
    expect(both).toHaveLength(1)
  })

  it("names untagged images by short id and tones states", () => {
    expect(shortId("sha256:abcdef0123456789")).toBe("abcdef012345")
    expect(imageLabel({ id: "sha256:abcdef0123456789", tags: [] })).toBe("<none> abcdef012345")
    expect(stateTone("running")).toBe("success")
    expect(stateTone("restarting")).toBe("warning")
    expect(stateTone("exited")).toBe("outline")
  })
})

describe("DockerPage", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) (fn as ReturnType<typeof vi.fn>).mockReset()
  })

  it("shows the engine facts and the container list", async () => {
    setup([container(), container({ id: "z", name: "old", state: "exited", status: "Exited (0)" })])
    expect(await screen.findByText("29.9.0")).toBeInTheDocument()
    expect(screen.getByText("2 of 3")).toBeInTheDocument()
    expect(await containerRow("redis")).toBeTruthy()
    expect((await containerRow("old")).getByText("exited")).toBeInTheDocument()
  })

  it("explains an unreachable Docker instead of showing an empty page", async () => {
    vi.mocked(api.dockerInfo).mockRejectedValue(
      new ApiError(503, "docker_unreachable", "Docker is not reachable; is the Docker service running?"),
    )
    mount()
    expect(await screen.findByText(/Docker is not reachable/)).toBeInTheDocument()
  })

  it("offers Start for a stopped container and Stop and Restart for a running one", async () => {
    setup([container(), container({ id: "z", name: "old", state: "exited" })])
    const running = await containerRow("redis")
    expect(running.getByRole("button", { name: "Stop redis" })).toBeInTheDocument()
    expect(running.getByRole("button", { name: "Restart redis" })).toBeInTheDocument()
    expect(running.queryByRole("button", { name: "Start redis" })).toBeNull()
    const stopped = await containerRow("old")
    expect(stopped.getByRole("button", { name: "Start old" })).toBeInTheDocument()
    expect(stopped.queryByRole("button", { name: "Stop old" })).toBeNull()
  })

  it("sends the chosen control to the server", async () => {
    vi.mocked(api.dockerControl).mockResolvedValue(undefined)
    setup()
    const user = userEvent.setup()
    await user.click((await containerRow("redis")).getByRole("button", { name: "Stop redis" }))
    await waitFor(() => expect(api.dockerControl).toHaveBeenCalledWith("aaaa1111bbbb2222cccc3333", "stop"))
  })

  it("does not offer stop, kill or remove for the panel's own containers", async () => {
    setup([container({ name: "panel-nginx", managed: true })])
    const row = await containerRow("panel-nginx")
    expect(row.getByText("Panel")).toBeInTheDocument()
    expect(row.getByRole("button", { name: "Restart panel-nginx" })).toBeInTheDocument()
    for (const verb of ["Stop", "Kill", "Remove"]) {
      expect(row.queryByRole("button", { name: `${verb} panel-nginx` })).toBeNull()
    }
  })

  it("asks before killing and before removing, and does nothing on cancel", async () => {
    setup()
    const user = userEvent.setup()
    await user.click((await containerRow("redis")).getByRole("button", { name: "Kill redis" }))
    expect(await screen.findByText("Kill redis?")).toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Cancel" }))
    expect(api.dockerControl).not.toHaveBeenCalled()

    vi.mocked(api.dockerRemoveContainer).mockResolvedValue(undefined)
    await user.click((await containerRow("redis")).getByRole("button", { name: "Remove redis" }))
    await user.click(await screen.findByRole("button", { name: "Remove container" }))
    await waitFor(() => expect(api.dockerRemoveContainer).toHaveBeenCalledWith("aaaa1111bbbb2222cccc3333", true))
  })

  it("shows the server's sentence when a control is refused", async () => {
    vi.mocked(api.dockerControl).mockRejectedValue(new ApiError(409, "conflict", "container is paused"))
    setup()
    const user = userEvent.setup()
    await user.click((await containerRow("redis")).getByRole("button", { name: "Restart redis" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("container is paused")
  })

  it("opens a container's logs", async () => {
    vi.mocked(api.dockerLogs).mockResolvedValue({ logs: "ready to accept connections\n", truncated: false })
    setup()
    const user = userEvent.setup()
    await user.click((await containerRow("redis")).getByRole("button", { name: "Details of redis" }))
    expect(await screen.findByLabelText("Container logs")).toHaveTextContent("ready to accept connections")
  })

  it("monitors running containers, busiest first", async () => {
    setup()
    const user = userEvent.setup()
    await user.click(await screen.findByRole("tab", { name: "Monitor" }))
    await screen.findAllByTestId("docker-stats-row")
    const names = screen.getAllByTestId("docker-stats-row").map((r) => r.getAttribute("data-container"))
    expect(names).toEqual(["busy", "light"])
    expect(screen.getByRole("meter", { name: "CPU of busy" })).toHaveAttribute("aria-valuenow", "90")
  })

  it("disables removing an image or volume that something still uses", async () => {
    setup()
    const user = userEvent.setup()
    await user.click(await screen.findByRole("tab", { name: "Images" }))
    expect(await screen.findByRole("button", { name: "Remove app:1" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Remove redis:7" })).toBeEnabled()

    await user.click(screen.getByRole("tab", { name: "Volumes" }))
    expect(await screen.findByRole("button", { name: "Remove data" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Remove spare" })).toBeEnabled()
  })
})
