import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, describe, expect, it, vi } from "vitest"

import { CreateSiteForm } from "@/components/CreateSiteDialog"
import { SITES_ROOT } from "@/components/FolderPicker"
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog"
import { api, type FMEntry, type FMListing } from "@/lib/api"

const dir = (parent: string, name: string): FMEntry => ({
  name,
  path: `${parent}/${name}`,
  kind: "dir",
  size: 0,
  mode: 0o755,
  uid: 0,
  gid: 0,
  owner: "root",
  group: "root",
  mtime: 0,
})
const file = (parent: string, name: string): FMEntry => ({ ...dir(parent, name), kind: "file" })

const listing = (path: string, entries: FMEntry[]): FMListing => ({
  path,
  entries,
  total: entries.length,
  offset: 0,
})

const BLOG = `${SITES_ROOT}/blog`
const tree: Record<string, FMEntry[]> = {
  [SITES_ROOT]: [dir(SITES_ROOT, "blog"), dir(SITES_ROOT, "shop"), file(SITES_ROOT, "notes.txt")],
  [BLOG]: [dir(BLOG, "public")],
}

const mockTree = () =>
  vi.spyOn(api, "fmList").mockImplementation(async ({ path }) => listing(path, tree[path] ?? []))

const renderForm = () => {
  const onSubmit = vi.fn()
  render(<CreateSiteForm onSubmit={onSubmit} onCancel={() => {}} pending={false} error={null} />)
  return onSubmit
}

const picker = () => screen.queryByRole("dialog", { name: "Choose a folder" })

afterEach(() => vi.restoreAllMocks())

describe("folder picker dialog", () => {
  it("is closed until Browse is pressed, then opens as a dialog at the sites root", async () => {
    const fm = mockTree()
    const user = userEvent.setup()
    renderForm()

    expect(picker()).not.toBeInTheDocument()
    expect(fm).not.toHaveBeenCalled()

    await user.click(screen.getByRole("button", { name: "Browse…" }))

    const dialog = await screen.findByRole("dialog", { name: "Choose a folder" })
    expect(dialog).toHaveTextContent(SITES_ROOT)
    expect(await screen.findByRole("button", { name: /blog/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /shop/ })).toBeInTheDocument()
    // Folders only: files are not offered.
    expect(screen.queryByText("notes.txt")).not.toBeInTheDocument()
    expect(fm).toHaveBeenCalledWith({ path: SITES_ROOT, sort: "name" })
  })

  it("browses into sub-folders, fills the field, closes, and submits the chosen root", async () => {
    mockTree()
    const user = userEvent.setup()
    const onSubmit = renderForm()

    await user.click(screen.getByRole("button", { name: "Browse…" }))
    await user.click(await screen.findByRole("button", { name: /blog/ }))
    await user.click(await screen.findByRole("button", { name: /public/ }))
    await user.click(screen.getByRole("button", { name: "Use this folder" }))

    await waitFor(() => expect(picker()).not.toBeInTheDocument())
    expect(screen.getByLabelText("Folder")).toHaveValue(`${BLOG}/public`)

    await user.type(screen.getByLabelText("Domain"), "a.test")
    await user.click(screen.getByRole("button", { name: "Create site" }))
    expect(onSubmit).toHaveBeenCalledWith({
      domain: "a.test",
      aliases: [],
      kind: "static",
      root: `${BLOG}/public`,
    })
  })

  it("goes up a level, and cannot go above the sites root", async () => {
    mockTree()
    const user = userEvent.setup()
    renderForm()

    await user.click(screen.getByRole("button", { name: "Browse…" }))
    const up = screen.getByRole("button", { name: "Up one folder" })
    expect(up).toBeDisabled()

    await user.click(await screen.findByRole("button", { name: /blog/ }))
    await screen.findByRole("button", { name: /public/ })
    expect(up).toBeEnabled()

    await user.click(up)
    expect(await screen.findByRole("button", { name: /shop/ })).toBeInTheDocument()
    expect(up).toBeDisabled()
  })

  it("Cancel closes it and leaves the field as it was", async () => {
    mockTree()
    const user = userEvent.setup()
    renderForm()

    await user.type(screen.getByLabelText("Folder"), "mine")
    await user.click(screen.getByRole("button", { name: "Browse…" }))
    await user.click(await screen.findByRole("button", { name: /shop/ }))
    await user.click(screen.getByRole("button", { name: "Cancel", hidden: false }))

    await waitFor(() => expect(picker()).not.toBeInTheDocument())
    expect(screen.getByLabelText("Folder")).toHaveValue("mine")
  })

  it("reopens at the folder already typed, not where it was left", async () => {
    const fm = mockTree()
    const user = userEvent.setup()
    renderForm()

    await user.click(screen.getByRole("button", { name: "Browse…" }))
    await user.click(await screen.findByRole("button", { name: /shop/ }))
    await user.click(screen.getByRole("button", { name: "Use this folder" }))
    await waitFor(() => expect(picker()).not.toBeInTheDocument())
    expect(screen.getByLabelText("Folder")).toHaveValue(`${SITES_ROOT}/shop`)

    fm.mockClear()
    await user.clear(screen.getByLabelText("Folder"))
    await user.type(screen.getByLabelText("Folder"), BLOG)
    await user.click(screen.getByRole("button", { name: "Browse…" }))

    expect(await screen.findByRole("button", { name: /public/ })).toBeInTheDocument()
    expect(fm).toHaveBeenCalledWith({ path: BLOG, sort: "name" })
  })

  it("ignores a typed folder outside the sites root and starts at the root", async () => {
    const fm = mockTree()
    const user = userEvent.setup()
    renderForm()

    await user.type(screen.getByLabelText("Folder"), "/etc")
    await user.click(screen.getByRole("button", { name: "Browse…" }))

    await screen.findByRole("button", { name: /blog/ })
    expect(fm).toHaveBeenCalledWith({ path: SITES_ROOT, sort: "name" })
    expect(fm).not.toHaveBeenCalledWith(expect.objectContaining({ path: "/etc" }))
  })

  it("says so when the sites root does not exist yet", async () => {
    vi.spyOn(api, "fmList").mockRejectedValue(new Error("not found"))
    const user = userEvent.setup()
    renderForm()

    await user.click(screen.getByRole("button", { name: "Browse…" }))

    expect(await screen.findByRole("status")).toHaveTextContent("does not exist yet")
    // It can still be closed, and the field is still typeable.
    await user.click(screen.getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(picker()).not.toBeInTheDocument())
  })

  it("does not submit the add-site form when its buttons are pressed", async () => {
    mockTree()
    const user = userEvent.setup()
    const onSubmit = renderForm()

    await user.type(screen.getByLabelText("Domain"), "a.test")
    await user.click(screen.getByRole("button", { name: "Browse…" }))
    await user.click(await screen.findByRole("button", { name: /blog/ }))
    await user.click(screen.getByRole("button", { name: "Up one folder" }))
    await user.click(screen.getByRole("button", { name: "Use this folder" }))

    await waitFor(() => expect(picker()).not.toBeInTheDocument())
    expect(onSubmit).not.toHaveBeenCalled()
  })

  describe("inside the Add a site dialog", () => {
    const renderNested = () => {
      const onSubmit = vi.fn()
      render(
        <Dialog open>
          <DialogContent>
            <DialogTitle>Add a site</DialogTitle>
            <DialogDescription>Outer dialog</DialogDescription>
            <CreateSiteForm
              onSubmit={onSubmit}
              onCancel={() => {}}
              pending={false}
              error={null}
            />
          </DialogContent>
        </Dialog>,
      )
      return onSubmit
    }

    it("stacks over it, and Escape closes only the picker", async () => {
      mockTree()
      const user = userEvent.setup()
      renderNested()

      await user.click(screen.getByRole("button", { name: "Browse…" }))
      expect(await screen.findByRole("dialog", { name: "Choose a folder" })).toBeInTheDocument()

      await user.keyboard("{Escape}")

      await waitFor(() => expect(picker()).not.toBeInTheDocument())
      expect(screen.getByRole("dialog", { name: "Add a site" })).toBeInTheDocument()
      expect(screen.getByLabelText("Folder")).toBeInTheDocument()
    })

    it("fills the field of the outer dialog and leaves it open", async () => {
      mockTree()
      const user = userEvent.setup()
      renderNested()

      await user.click(screen.getByRole("button", { name: "Browse…" }))
      await user.click(await screen.findByRole("button", { name: /shop/ }))
      await user.click(screen.getByRole("button", { name: "Use this folder" }))

      await waitFor(() => expect(picker()).not.toBeInTheDocument())
      expect(screen.getByRole("dialog", { name: "Add a site" })).toBeInTheDocument()
      expect(screen.getByLabelText("Folder")).toHaveValue(`${SITES_ROOT}/shop`)
    })
  })
})
