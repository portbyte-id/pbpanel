import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { TrashPanel } from "@/components/fm/TrashPanel"
import { api, type FMTrashItem } from "@/lib/api"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      fmTrash: vi.fn(),
      fmRestore: vi.fn(),
      fmPurge: vi.fn(),
    },
  }
})

const item = (over: Partial<FMTrashItem> = {}): FMTrashItem => ({
  id: "t1",
  name: "old.txt",
  orig_path: "/var/www/old.txt",
  size: 12,
  is_dir: false,
  mode: 0o644,
  uid: 0,
  gid: 0,
  deleted_at: "2026-11-17T10:00:00.000Z",
  ...over,
})

describe("TrashPanel", () => {
  beforeEach(() => {
    vi.mocked(api.fmTrash).mockReset()
    vi.mocked(api.fmRestore).mockReset()
    vi.mocked(api.fmPurge).mockReset()
  })

  it("lists the bin with original locations and deleted dates", async () => {
    vi.mocked(api.fmTrash).mockResolvedValue([item()])
    render(<TrashPanel onChanged={() => {}} />)

    const table = await screen.findByRole("table")
    expect(within(table).getByText("old.txt")).toBeInTheDocument()
    expect(within(table).getByText("/var/www/old.txt")).toBeInTheDocument()
    expect(within(table).getByText("12 B")).toBeInTheDocument()
  })

  it("shows an empty sentence when nothing is in the bin", async () => {
    vi.mocked(api.fmTrash).mockResolvedValue([])
    render(<TrashPanel onChanged={() => {}} />)

    expect(await screen.findByText(/trash is empty/)).toBeInTheDocument()
  })

  it("restores without a confirmation and refreshes the bin", async () => {
    vi.mocked(api.fmTrash).mockResolvedValue([item()])
    vi.mocked(api.fmRestore).mockResolvedValue({ path: "/var/www/old.txt" })
    const user = userEvent.setup()
    render(<TrashPanel onChanged={() => {}} />)

    await user.click(await screen.findByRole("button", { name: "Restore old.txt" }))

    await waitFor(() => expect(api.fmRestore).toHaveBeenCalledWith("t1"))
    // The list is re-read after a restore.
    await waitFor(() => expect(api.fmTrash).toHaveBeenCalledTimes(2))
  })

  it("requires the typed name before a permanent delete fires", async () => {
    vi.mocked(api.fmTrash).mockResolvedValue([item()])
    vi.mocked(api.fmPurge).mockResolvedValue(undefined)
    const user = userEvent.setup()
    render(<TrashPanel onChanged={() => {}} />)

    await user.click(await screen.findByRole("button", { name: "Delete old.txt permanently" }))

    const dialog = screen.getByRole("dialog")
    const action = within(dialog).getByRole("button", { name: "Delete forever" })
    expect(action).toBeDisabled()
    expect(api.fmPurge).not.toHaveBeenCalled()

    await user.type(within(dialog).getByLabelText(/to confirm/), "old.txt")
    await user.click(action)

    await waitFor(() => expect(api.fmPurge).toHaveBeenCalledWith("t1", "old.txt"))
  })

  it("keeps the item when the purge is refused, showing the server's sentence", async () => {
    vi.mocked(api.fmTrash).mockResolvedValue([item()])
    vi.mocked(api.fmPurge).mockRejectedValue(
      new (await import("@/lib/api")).ApiError(403, "protected_path", "refused"),
    )
    const user = userEvent.setup()
    render(<TrashPanel onChanged={() => {}} />)

    await user.click(await screen.findByRole("button", { name: "Delete old.txt permanently" }))
    await user.type(screen.getByLabelText(/to confirm/), "old.txt")
    await user.click(screen.getByRole("button", { name: "Delete forever" }))

    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument())
    // The dialog closes on a failure; the row stays.
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(screen.getByText("old.txt")).toBeInTheDocument()
  })
})
