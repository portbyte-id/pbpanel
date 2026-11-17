import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { ConfirmDialog, ExtractDialog, NameDialog } from "@/components/fm/FMDialogs"
import { crumbs, formatMode, formatSize, uploadStateLine } from "@/components/fm/FMShared"
import { api } from "@/lib/api"
import type { UploadTask } from "@/lib/uploadQueue"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      fmMkdir: vi.fn(async (path: string) => ({ path })),
      fmCreateFile: vi.fn(async (path: string) => ({ path })),
      fmRename: vi.fn(async (path: string, name: string) => ({ path, name })),
    },
  }
})

describe("formatSize", () => {
  it("shows a dash for a folder and plain bytes below a kibibyte", () => {
    expect(formatSize(900, false)).toBe("900 B")
    expect(formatSize(0, true)).toBe("—")
  })

  it("keeps one decimal below ten and rounds above", () => {
    expect(formatSize(1536, false)).toBe("1.5 KiB")
    expect(formatSize(1536 * 1024, false)).toBe("1.5 MiB")
    expect(formatSize(15 * 1024 * 1024, false)).toBe("15 MiB")
  })
})

describe("formatMode", () => {
  it("prints the permission bits in octal, without the file-type bits", () => {
    expect(formatMode(0o100644)).toBe("644")
    expect(formatMode(0o040755)).toBe("755")
  })
})

describe("crumbs", () => {
  it("starts at the root and builds each part", () => {
    expect(crumbs("/var/www/html")).toEqual([
      { label: "/", path: "/" },
      { label: "var", path: "/var" },
      { label: "www", path: "/var/www" },
      { label: "html", path: "/var/www/html" },
    ])
  })

  it("renders the root alone", () => {
    expect(crumbs("/")).toEqual([{ label: "/", path: "/" }])
  })
})

describe("uploadStateLine", () => {
  const task = (over: Partial<UploadTask>): UploadTask => ({
    key: "u1",
    dir: "/tmp",
    file: new File(["abc"], "a.txt"),
    status: "queued",
    sent: 0,
    uploadId: null,
    error: null,
    path: null,
    ...over,
  })

  it("reports progress as a whole percentage", () => {
    expect(uploadStateLine(task({ status: "uploading", sent: 1 }))).toBe("33%")
  })

  it("shows the server's sentence on a failure", () => {
    expect(uploadStateLine(task({ status: "failed", error: "no_space: full" }))).toBe(
      "no_space: full",
    )
  })
})

describe("ConfirmDialog", () => {
  it("keeps the action disabled until the name is typed exactly", async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    render(
      <ConfirmDialog
        title="Delete build?"
        description="It moves to the trash."
        confirmText="build"
        actionLabel="Move to trash"
        busy={false}
        error={null}
        onConfirm={onConfirm}
        onOpenChange={() => {}}
      />,
    )

    const action = screen.getByRole("button", { name: "Move to trash" })
    expect(action).toBeDisabled()

    await user.type(screen.getByLabelText(/to confirm/), "buil")
    expect(action).toBeDisabled()

    await user.type(screen.getByLabelText(/to confirm/), "d")
    expect(action).toBeEnabled()

    await user.click(action)
    expect(onConfirm).toHaveBeenCalledOnce()
  })

  it("shows the server's refusal as it came", () => {
    render(
      <ConfirmDialog
        title="Delete"
        description="d"
        confirmText="x"
        actionLabel="Go"
        busy={false}
        error="confirmation does not match"
        onConfirm={() => {}}
        onOpenChange={() => {}}
      />,
    )
    expect(screen.getByRole("alert")).toHaveTextContent("confirmation does not match")
  })

  it("needs no typed text when the action is not confirmed by name", () => {
    render(
      <ConfirmDialog
        title="Delete two files?"
        description="d"
        actionLabel="Move to trash"
        busy={false}
        error={null}
        onConfirm={() => {}}
        onOpenChange={() => {}}
      />,
    )
    expect(screen.queryByLabelText(/to confirm/)).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Move to trash" })).toBeEnabled()
  })
})

describe("NameDialog", () => {
  it("creates a folder at the current path", async () => {
    const user = userEvent.setup()
    const onDone = vi.fn()
    render(<NameDialog kind="mkdir" path="/var/www" onDone={onDone} onOpenChange={() => {}} />)

    await user.type(screen.getByLabelText("Name"), "releases")
    await user.click(screen.getByRole("button", { name: "New folder" }))

    expect(api.fmMkdir).toHaveBeenCalledWith("/var/www/releases")
    expect(onDone).toHaveBeenCalledOnce()
  })

  it("creates at the root without a doubled slash", async () => {
    const user = userEvent.setup()
    render(<NameDialog kind="create" path="/" onDone={() => {}} onOpenChange={() => {}} />)

    await user.type(screen.getByLabelText("Name"), "notes.txt")
    await user.click(screen.getByRole("button", { name: "New file" }))

    expect(api.fmCreateFile).toHaveBeenCalledWith("/notes.txt")
  })

  it("prefills a rename and sends the new name", async () => {
    const user = userEvent.setup()
    render(
      <NameDialog
        kind="rename"
        path="/var/www/old.txt"
        initialName="old.txt"
        onDone={() => {}}
        onOpenChange={() => {}}
      />,
    )

    const field = screen.getByLabelText("Name")
    expect(field).toHaveValue("old.txt")
    await user.clear(field)
    await user.type(field, "new.txt")
    await user.click(screen.getByRole("button", { name: "Rename" }))

    expect(api.fmRename).toHaveBeenCalledWith("/var/www/old.txt", "new.txt")
  })
})

describe("ExtractDialog", () => {
  it("builds the destination beside the archive and submits it", async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    render(
      <ExtractDialog
        archive="/var/www/site.tar.gz"
        parent="/var/www"
        defaultName="site"
        busy={false}
        error={null}
        onConfirm={onConfirm}
        onOpenChange={() => {}}
      />,
    )

    const field = screen.getByLabelText(/folder named/)
    expect(field).toHaveValue("site")
    await user.clear(field)
    await user.type(field, "unpacked")
    await user.click(screen.getByRole("button", { name: "Extract" }))

    expect(onConfirm).toHaveBeenCalledWith("/var/www/unpacked")
  })

  it("shows a refusal inside the dialog", () => {
    render(
      <ExtractDialog
        archive="/a.zip"
        parent="/"
        defaultName="a"
        busy={false}
        error="unsafe or invalid archive"
        onConfirm={() => {}}
        onOpenChange={() => {}}
      />,
    )
    const dialog = screen.getByRole("dialog")
    expect(within(dialog).getByRole("alert")).toHaveTextContent("unsafe or invalid archive")
  })
})
