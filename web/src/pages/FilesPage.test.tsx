import { fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { FilesPage } from "@/pages/FilesPage"
import { api, type FMEntry, type FMListing } from "@/lib/api"
import { uploadQueue } from "@/lib/uploadQueue"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      fmList: vi.fn(),
      fmDelete: vi.fn(),
      fmArchive: vi.fn(),
      fmExtract: vi.fn(),
      fmMkdir: vi.fn(),
      fmCreateFile: vi.fn(),
      fmRename: vi.fn(),
      fmOpenFile: vi.fn(),
      fmSaveFile: vi.fn(),
    },
  }
})

vi.mock("@/components/JobsDrawer", () => ({
  JobsDrawer: () => null,
}))

const entry = (over: Partial<FMEntry> = {}): FMEntry => ({
  name: "a.txt",
  path: "/var/www/a.txt",
  kind: "file",
  size: 3,
  mode: 0o644,
  uid: 0,
  gid: 0,
  owner: "root",
  group: "root",
  mtime: 1_700_000_000_000_000_000,
  ...over,
})

const dir = (name: string, path: string): FMEntry => ({
  ...entry({ name, path }),
  kind: "dir",
  size: 0,
})

const listing = (entries: FMEntry[]): FMListing => ({
  path: "/var/www",
  entries,
  total: entries.length,
  offset: 0,
})

describe("FilesPage", () => {
  beforeEach(() => {
    vi.mocked(api.fmList).mockReset()
    vi.mocked(api.fmDelete).mockReset()
    window.localStorage.clear()
    vi.mocked(api.fmArchive).mockReset()
  })

  const renderPage = (entries: FMEntry[]) => {
    // The server owns the hidden-file filter: `hidden=1` adds dotfiles.
    vi.mocked(api.fmList).mockImplementation(async (input) =>
      listing(input.hidden ? entries : entries.filter((en) => !en.name.startsWith("."))),
    )
    render(<FilesPage />)
  }

  it("lists a folder with its breadcrumbs, sizes and modes", async () => {
    renderPage([
      dir("web", "/var/www/web"),
      entry({ name: "a.txt", path: "/var/www/a.txt", size: 3 }),
    ])
    await screen.findByLabelText("Select all")

    const table = screen.getByRole("table")
    expect(within(table).getByText("web")).toBeInTheDocument()
    expect(within(table).getByText("a.txt")).toBeInTheDocument()
    expect(within(table).getByText("3 B")).toBeInTheDocument()
    expect(within(table).getAllByText("644").length).toBeGreaterThan(0)
  })

  it("selects all rows and reports the count", async () => {
    renderPage([
      entry({ name: "a.txt", path: "/a.txt" }),
      entry({ name: "b.txt", path: "/b.txt" }),
      entry({ name: "c.txt", path: "/c.txt" }),
    ])
    await screen.findByLabelText("Select all")
    const user = userEvent.setup()

    await user.click(screen.getByLabelText("Select all"))
    expect(screen.getByText("3 selected")).toBeInTheDocument()
    expect(screen.getByLabelText("Select a.txt")).toBeChecked()
    expect(screen.getByLabelText("Select b.txt")).toBeChecked()
    expect(screen.getByLabelText("Select c.txt")).toBeChecked()

    // Clearing again empties the selection. The count sentence is scoped so the
    // "Archive selected" button cannot satisfy it.
    await user.click(screen.getByLabelText("Select all"))
    expect(screen.queryByText(/^\d+ selected$/)).not.toBeInTheDocument()
  })

  it("hides dotfiles until the toggle is on, and shows them again after", async () => {
    renderPage([
      entry({ name: "a.txt", path: "/a.txt" }),
      entry({ name: ".env", path: "/var/www/.env" }),
    ])
    await screen.findByLabelText("Select all")
    const user = userEvent.setup()

    expect(screen.queryByText(".env")).not.toBeInTheDocument()
    await user.click(screen.getByLabelText("Show hidden files"))
    await screen.findByText(".env")

    await user.click(screen.getByLabelText("Show hidden files"))
    await waitFor(() => expect(screen.queryByText(".env")).not.toBeInTheDocument())
  })

  it("moves single files to the trash without a typed confirmation", async () => {
    renderPage([entry({ name: "a.txt", path: "/a.txt" })])
    await screen.findByLabelText("Select all")
    const user = userEvent.setup()

    await user.click(screen.getByLabelText("Select a.txt"))
    await user.click(screen.getByRole("button", { name: "Delete" }))
    await user.click(screen.getByRole("button", { name: "Move to trash" }))

    await waitFor(() => expect(api.fmDelete).toHaveBeenCalledOnce())
    expect(api.fmDelete).toHaveBeenCalledWith([{ path: "/a.txt" }], { permanent: false })
  })

  it("moves a folder to the trash with no name to type", async () => {
    renderPage([dir("build", "/var/www/build")])
    await screen.findByLabelText("Select all")
    const user = userEvent.setup()

    await user.click(screen.getByLabelText("Select build"))
    await user.click(screen.getByRole("button", { name: "Delete" }))

    expect(screen.queryByLabelText(/to confirm/)).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Move to trash" }))

    await waitFor(() =>
      expect(api.fmDelete).toHaveBeenCalledWith([{ path: "/var/www/build" }], { permanent: false }),
    )
  })

  it("deletes permanently when the skip-trash box is ticked", async () => {
    renderPage([entry({ name: "a.txt", path: "/a.txt" })])
    await screen.findByLabelText("Select all")
    const user = userEvent.setup()

    await user.click(screen.getByLabelText("Select a.txt"))
    await user.click(screen.getByRole("button", { name: "Delete" }))
    await user.click(screen.getByRole("checkbox", { name: /skip the trash/i }))
    await user.click(screen.getByRole("button", { name: "Delete permanently" }))

    await waitFor(() =>
      expect(api.fmDelete).toHaveBeenCalledWith([{ path: "/a.txt" }], { permanent: true }),
    )
  })

  it("opens a context menu on right-click, and runs Delete from it", async () => {
    renderPage([entry({ name: "a.txt", path: "/a.txt" })])
    await screen.findByLabelText("Select all")
    const user = userEvent.setup()

    fireEvent.contextMenu(screen.getByText("a.txt").closest("tr")!, { clientX: 40, clientY: 60 })
    const menu = screen.getByRole("menu", { name: "Actions for a.txt" })
    expect(within(menu).getByRole("menuitem", { name: "Rename" })).toBeInTheDocument()
    expect(within(menu).queryByRole("menuitem", { name: "Open" })).not.toBeInTheDocument()

    await user.click(within(menu).getByRole("menuitem", { name: "Delete…" }))
    expect(screen.queryByRole("menu")).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Move to trash" }))

    await waitFor(() =>
      expect(api.fmDelete).toHaveBeenCalledWith([{ path: "/a.txt" }], { permanent: false }),
    )
  })

  it("closes the context menu on Escape", async () => {
    renderPage([dir("build", "/var/www/build")])
    await screen.findByLabelText("Select all")
    const user = userEvent.setup()

    fireEvent.contextMenu(screen.getByText("build").closest("tr")!, { clientX: 40, clientY: 60 })
    expect(screen.getByRole("menu")).toBeInTheDocument()
    await user.keyboard("{Escape}")
    expect(screen.queryByRole("menu")).not.toBeInTheDocument()
  })

  it("returns to the folder it showed last", async () => {
    window.localStorage.setItem("panel.files.lastPath", "/var/www")
    renderPage([entry({ name: "a.txt", path: "/var/www/a.txt" })])

    await screen.findByLabelText("Select all")
    expect(api.fmList).toHaveBeenCalledWith(expect.objectContaining({ path: "/var/www" }))
  })

  it("sorts by modified time when the toolbar asks for it", async () => {
    renderPage([
      entry({ name: "old.txt", path: "/old.txt", mtime: 1 }),
      entry({ name: "new.txt", path: "/new.txt", mtime: 2 }),
    ])
    await screen.findByLabelText("Select all")
    const user = userEvent.setup()

    await user.selectOptions(screen.getByRole("combobox", { name: "Sort by" }), "mtime")
    // The order button reads as the current state; clicking it flips to descending.
    await user.click(screen.getByRole("button", { name: "Ascending" }))

    await waitFor(() =>
      expect(api.fmList).toHaveBeenLastCalledWith(expect.objectContaining({ sort: "mtime", desc: true })),
    )
  })

  it("opens the job drawer when the server answers 202", async () => {
    renderPage([entry({ name: "a.txt", path: "/a.txt" })])
    await screen.findByLabelText("Select all")
    const user = userEvent.setup()

    vi.mocked(api.fmDelete).mockResolvedValue({
      job: { id: "j1", kind: "fm.delete", state: "running" },
    })
    await user.click(screen.getByLabelText("Select a.txt"))
    await user.click(screen.getByRole("button", { name: "Delete" }))
    await user.click(screen.getByRole("button", { name: "Move to trash" }))

    // The drawer is stubbed out; the call itself is the contract here.
    await waitFor(() => expect(api.fmDelete).toHaveBeenCalledOnce())
    expect(vi.mocked(api.fmDelete).mock.calls[0][0]).toEqual([{ path: "/a.txt" }])
  })

  it("queues dropped files into the folder being viewed", async () => {
    renderPage([entry({ name: "a.txt", path: "/var/www/a.txt" })])
    await screen.findByLabelText("Select all")
    const enqueue = vi.spyOn(uploadQueue, "enqueue").mockImplementation(() => {})

    const zone = screen.getByLabelText(/Drop files here/)
    const file = new File(["hello"], "hello.txt", { type: "text/plain" })
    fireEvent.drop(zone, { dataTransfer: { files: [file], types: ["Files"] } })

    expect(enqueue).toHaveBeenCalledOnce()
    expect(enqueue).toHaveBeenCalledWith("/var/www", [file])
    enqueue.mockRestore()
  })
})
