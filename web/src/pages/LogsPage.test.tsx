import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { api, ApiError } from "@/lib/api"
import type { LogSource } from "@/lib/logs"
import { LogsPage } from "@/pages/LogsPage"
import { openStreamResponse, streamOf } from "@/test/helpers"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return { ...actual, api: { logSources: vi.fn(), openStream: vi.fn() } }
})

const sources: LogSource[] = [
  { id: "panel:audit", label: "Audit log", kind: "audit" },
  { id: "site:b.test:access", label: "b.test access", kind: "site" },
  { id: "site:a.test:access", label: "a.test access", kind: "site" },
  { id: "docker:panel-nginx", label: "panel-nginx", kind: "docker" },
]

const line = (s: string) => `event: line\ndata: ${JSON.stringify({ line: s })}\n\n`

function setup() {
  vi.mocked(api.logSources).mockResolvedValue(sources)
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <LogsPage />
    </QueryClientProvider>,
  )
}

const pathOf = (call: number) => String(vi.mocked(api.openStream).mock.calls[call]?.[0])

describe("LogsPage", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) (fn as ReturnType<typeof vi.fn>).mockReset()
  })

  it("groups the sources by kind and filters them", async () => {
    setup()
    const user = userEvent.setup()
    const nav = await screen.findByRole("navigation", { name: "Log sources" })
    const groups = within(nav)
      .getAllByRole("region")
      .map((g) => g.getAttribute("aria-label"))
    expect(groups).toEqual(["Audit", "Containers", "Sites"])
    expect(within(nav).getAllByRole("button").map((b) => b.textContent)).toEqual([
      "Audit log",
      "panel-nginx",
      "a.test access",
      "b.test access",
    ])

    await user.type(screen.getByLabelText("Filter logs"), "b.test")
    expect(within(nav).getAllByRole("button").map((b) => b.textContent)).toEqual(["b.test access"])
    await user.clear(screen.getByLabelText("Filter logs"))
    await user.type(screen.getByLabelText("Filter logs"), "zzz")
    expect(within(nav).getByText(/No log matches/)).toBeInTheDocument()
  })

  it("tails the chosen log with follow on, and pauses without losing lines", async () => {
    setup()
    const user = userEvent.setup()
    const stream = openStreamResponse()
    vi.mocked(api.openStream).mockResolvedValue(stream.response)

    await user.click(await screen.findByRole("button", { name: "a.test access" }))
    await waitFor(() => expect(api.openStream).toHaveBeenCalled())
    expect(pathOf(0)).toBe("/api/logs/site%3Aa.test%3Aaccess/tail?lines=200&follow=1")

    stream.push(line("GET / 200") + line("GET /x 404"))
    const log = await screen.findByLabelText("Log of a.test access")
    await waitFor(() => expect(log).toHaveTextContent("GET /x 404"))
    expect(screen.getByText("Following the log.")).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Pause" }))
    stream.push(line("GET /held 200"))
    await new Promise((r) => setTimeout(r, 120))
    expect(log).not.toHaveTextContent("GET /held")
    expect(screen.getByText(/Paused/)).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Resume" }))
    await waitFor(() => expect(log).toHaveTextContent("GET /held 200"))

    await user.click(screen.getByRole("button", { name: "Clear" }))
    expect(log).not.toHaveTextContent("GET /")
  })

  it("restarts the tail when the line count or follow changes", async () => {
    setup()
    const user = userEvent.setup()
    vi.mocked(api.openStream).mockImplementation(async () => streamOf([line("a"), "event: end\ndata: {}\n\n"]))

    await user.click(await screen.findByRole("button", { name: "Audit log" }))
    await waitFor(() => expect(api.openStream).toHaveBeenCalledTimes(1))

    await user.selectOptions(screen.getByLabelText("Lines"), "1000")
    await waitFor(() => expect(api.openStream).toHaveBeenCalledTimes(2))
    expect(pathOf(1)).toBe("/api/logs/panel%3Aaudit/tail?lines=1000&follow=1")

    await user.click(screen.getByRole("button", { name: "Follow" }))
    await waitFor(() => expect(api.openStream).toHaveBeenCalledTimes(3))
    expect(pathOf(2)).toBe("/api/logs/panel%3Aaudit/tail?lines=1000&follow=0")
    expect(screen.getByRole("button", { name: "Follow" })).toHaveAttribute("aria-pressed", "false")
    expect(await screen.findByText("Showing the last 1 lines.")).toBeInTheDocument()
  })

  it("shows the server's sentence when the log cannot be read and offers Reconnect", async () => {
    setup()
    const user = userEvent.setup()
    vi.mocked(api.openStream).mockRejectedValueOnce(
      new ApiError(409, "source_unavailable", "Docker is not reachable."),
    )
    vi.mocked(api.openStream).mockImplementation(async () => streamOf([line("back"), "event: end\ndata: {}\n\n"]))

    await user.click(await screen.findByRole("button", { name: "panel-nginx" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("Docker is not reachable.")
    await user.click(screen.getByRole("button", { name: "Reconnect" }))
    await waitFor(() => expect(screen.getByLabelText("Log of panel-nginx")).toHaveTextContent("back"))
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })

  it("searches with the chosen toggles and shows file:line, partial and the limit notice", async () => {
    setup()
    const user = userEvent.setup()
    vi.mocked(api.openStream).mockImplementation(async (path: string) => {
      if (path.includes("/search")) {
        return streamOf([
          '{"type":"match","file":"a.test.access.log.1","line":42,"partial":true,"text":"GET /login 200"}\n',
          '{"type":"match","file":"a.test.access.log","line":7,"text":"GET /login 302"}\n',
          '{"type":"done","stats":{"files":2,"bytesScanned":2097152,"matches":2,"limitHit":true,"windowCapped":true}}\n',
        ])
      }
      return streamOf(["event: end\ndata: {}\n\n"])
    })

    await user.click(await screen.findByRole("button", { name: "a.test access" }))
    await user.click(screen.getByRole("tab", { name: "Search" }))
    await user.type(screen.getByLabelText("Search for"), "GET /login")
    await user.click(screen.getByLabelText("Regular expression"))
    await user.click(screen.getByLabelText("Ignore case"))
    await user.selectOptions(screen.getByLabelText("Look back"), "64")
    await user.selectOptions(screen.getByLabelText("Stop after"), "100")
    await user.click(screen.getByRole("button", { name: "Search" }))

    expect(await screen.findByText("a.test.access.log.1:42")).toBeInTheDocument()
    expect(screen.getByText("a.test.access.log:7")).toBeInTheDocument()
    expect(screen.getByText("partial")).toBeInTheDocument()

    const searchCall = vi.mocked(api.openStream).mock.calls.find((c) => String(c[0]).includes("/search"))
    expect(searchCall?.[0]).toBe(
      "/api/logs/site%3Aa.test%3Aaccess/search?q=GET+%2Flogin&regex=1&ci=1&last_mb=64&limit=100",
    )
    expect(searchCall?.[1]).toBe("application/x-ndjson")

    const summary = await screen.findByText(/2 matches in 2 files/)
    expect(summary).toHaveTextContent("2.00 MiB scanned")
    expect(screen.getByText(/Stopped at the limit of 100 matches/)).toBeInTheDocument()
    expect(screen.getByText(/Only the last 64 MB was searched/)).toBeInTheDocument()
  })

  it("says so when nothing matched, and shows a bad pattern under the box", async () => {
    setup()
    const user = userEvent.setup()
    vi.mocked(api.openStream).mockImplementation(async (path: string) => {
      if (path.includes("q=%28")) {
        throw new ApiError(400, "invalid_input", "q: missing closing )", { field: "q" })
      }
      if (path.includes("/search")) {
        return streamOf([
          '{"type":"done","stats":{"files":1,"bytesScanned":10,"matches":0,"limitHit":false,"windowCapped":false}}\n',
        ])
      }
      return streamOf(["event: end\ndata: {}\n\n"])
    })

    await user.click(await screen.findByRole("button", { name: "Audit log" }))
    await user.click(screen.getByRole("tab", { name: "Search" }))
    const box = screen.getByLabelText("Search for")

    await user.type(box, "nope")
    await user.click(screen.getByRole("button", { name: "Search" }))
    expect(await screen.findByText("Nothing matched.")).toBeInTheDocument()

    await user.clear(box)
    await user.type(box, "(")
    await user.click(screen.getByRole("button", { name: "Search" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("missing closing )")
    expect(box).toHaveAttribute("aria-invalid", "true")
  })
})
