import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { EditorDialog } from "@/components/fm/EditorDialog"
import { ApiError, type FMEntry, type FMFile } from "@/lib/api"
import { api } from "@/lib/api"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      fmOpenFile: vi.fn(),
      fmSaveFile: vi.fn(),
      fmList: vi.fn(),
      fmCreateFile: vi.fn(),
    },
  }
})

// CodeMirror measures text through the browser's layout engine, which jsdom
// does not have. A textarea stands in for it: the dialog's own behaviour —
// tabs, draft, base, conflict — is what these tests are about.
vi.mock("@/components/fm/TextEditor", () => ({
  statusOf: () => ({ line: 1, col: 1, history: 0 }),
  TextEditor: ({
    value,
    onChange,
    label,
    readOnly,
  }: {
    value: string
    onChange?: (value: string) => void
    label: string
    readOnly?: boolean
  }) => (
    <textarea
      aria-label={label}
      value={value}
      readOnly={readOnly}
      onChange={(e) => onChange?.(e.target.value)}
    />
  ),
}))

const opened: FMFile & { language: string } = {
  path: "/var/www/a.conf",
  content: "server { }\n",
  size: 11,
  mtime: 1_700_000_000_000_000_000,
  hash: "hash-a",
  mode: 0o644,
  language: "nginx",
}

const other: FMFile & { language: string } = {
  path: "/var/www/b.html",
  content: "<h1>b</h1>\n",
  size: 11,
  mtime: 1_700_000_000_000_000_000,
  hash: "hash-b",
  mode: 0o644,
  language: "html",
}

function entry(name: string, kind: FMEntry["kind"]): FMEntry {
  return {
    name,
    path: `/var/www/${name}`,
    kind,
    size: 0,
    mode: 0o644,
    uid: 0,
    gid: 0,
    owner: "root",
    group: "root",
    mtime: 0,
  }
}

/** Types into the stubbed editor for a file, after it has loaded. */
async function typeIn(user: ReturnType<typeof userEvent.setup>, path: string, text: string) {
  const editor = await screen.findByLabelText(`Contents of ${path}`)
  await user.click(editor)
  await user.keyboard(text)
}

describe("EditorDialog", () => {
  beforeEach(() => {
    window.localStorage.clear()
    vi.mocked(api.fmOpenFile).mockReset()
    vi.mocked(api.fmSaveFile).mockReset()
    vi.mocked(api.fmCreateFile).mockReset()
    vi.mocked(api.fmList).mockReset()
    vi.mocked(api.fmOpenFile).mockResolvedValue(opened)
    vi.mocked(api.fmList).mockResolvedValue({
      path: "/var/www",
      entries: [entry("a.conf", "file"), entry("b.html", "file"), entry("sub", "dir")],
      total: 3,
      offset: 0,
    })
  })

  it("keeps Save disabled until the draft differs from the file", async () => {
    const user = userEvent.setup()
    render(<EditorDialog path={opened.path} onClose={() => {}} onSaved={() => {}} />)

    const editor = await screen.findByLabelText(`Contents of ${opened.path}`)
    expect(editor).toHaveValue(opened.content)
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()

    await user.click(editor)
    await user.keyboard("x")
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled()
    expect(screen.getByLabelText("unsaved changes")).toBeInTheDocument()
  })

  it("sends the base the file was opened with, and reports the save", async () => {
    vi.mocked(api.fmSaveFile).mockResolvedValue({ ...opened, content: "server { }\nx" })
    const user = userEvent.setup()
    const onSaved = vi.fn()
    render(<EditorDialog path={opened.path} onClose={() => {}} onSaved={onSaved} />)

    await typeIn(user, opened.path, "x")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() =>
      expect(api.fmSaveFile).toHaveBeenCalledWith(opened.path, "server { }\nx", {
        size: opened.size,
        mtime: opened.mtime,
        hash: opened.hash,
      }),
    )
    expect(onSaved).toHaveBeenCalledOnce()
    expect(await screen.findByRole("status")).toHaveTextContent("Saved a.conf.")
  })

  it("shows the disk copy beside the draft and offers an overwrite on a 409 conflict", async () => {
    const onDisk: FMFile = {
      ...opened,
      content: "server { listen 8080; }\n",
      hash: "hash-c",
      mtime: opened.mtime + 5,
    }
    vi.mocked(api.fmSaveFile).mockRejectedValue(
      new ApiError(409, "edit_conflict", "the file changed on disk since it was opened", {
        payload: {
          error: "edit_conflict",
          detail: "the file changed on disk since it was opened",
          current: onDisk,
        },
      }),
    )
    const user = userEvent.setup()
    render(<EditorDialog path={opened.path} onClose={() => {}} onSaved={() => {}} />)

    await typeIn(user, opened.path, "x")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Overwrite disk copy with my draft" }),
      ).toBeInTheDocument(),
    )
    expect(screen.getByRole("alert")).toHaveTextContent("changed on disk")
    expect(screen.getByLabelText("The file as it is on disk now")).toHaveValue(
      "server { listen 8080; }\n",
    )
    // The draft stays on the left while the conflict is open.
    expect(screen.getByLabelText(`Contents of ${opened.path}`)).toHaveValue("server { }\nx")
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()

    // Overwriting sends the disk copy's base, which is what makes it stick.
    vi.mocked(api.fmSaveFile).mockResolvedValue({ ...onDisk, content: "server { }\nx" })
    await user.click(screen.getByRole("button", { name: "Overwrite disk copy with my draft" }))
    await waitFor(() =>
      expect(api.fmSaveFile).toHaveBeenLastCalledWith(opened.path, "server { }\nx", {
        size: onDisk.size,
        mtime: onDisk.mtime,
        hash: onDisk.hash,
      }),
    )
  })

  it("loads the disk copy only when asked, and drops the draft", async () => {
    const onDisk: FMFile = { ...opened, content: "server { listen 8080; }\n", hash: "hash-c" }
    vi.mocked(api.fmSaveFile).mockRejectedValue(
      new ApiError(409, "edit_conflict", "changed", {
        payload: { error: "edit_conflict", detail: "changed", current: onDisk },
      }),
    )
    const user = userEvent.setup()
    render(<EditorDialog path={opened.path} onClose={() => {}} onSaved={() => {}} />)

    await typeIn(user, opened.path, "x")
    await user.click(screen.getByRole("button", { name: "Save" }))
    await user.click(await screen.findByRole("button", { name: "Load disk copy" }))

    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(screen.getByLabelText(`Contents of ${opened.path}`)).toHaveValue(
      "server { listen 8080; }\n",
    )
    expect(screen.queryByLabelText("The file as it is on disk now")).not.toBeInTheDocument()
  })

  it("shows a load failure instead of an editor", async () => {
    vi.mocked(api.fmOpenFile).mockRejectedValue(new ApiError(415, "binary_file", "binary file"))
    render(<EditorDialog path={opened.path} onClose={() => {}} onSaved={() => {}} />)

    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("binary file"))
    expect(screen.queryByLabelText(/Contents of/)).not.toBeInTheDocument()
  })

  it("opens a second file from the directory list in its own tab", async () => {
    vi.mocked(api.fmOpenFile).mockImplementation(async (p: string) =>
      p === other.path ? other : opened,
    )
    const user = userEvent.setup()
    render(<EditorDialog path={opened.path} onClose={() => {}} onSaved={() => {}} />)

    const list = await screen.findByRole("list", { name: "Files in this folder" })
    await user.click(within(list).getByRole("button", { name: "b.html" }))

    await waitFor(() => expect(api.fmOpenFile).toHaveBeenCalledWith(other.path))
    expect(await screen.findByLabelText(`Contents of ${other.path}`)).toHaveValue(other.content)
    expect(screen.getByRole("tab", { name: "b.html" })).toHaveAttribute("aria-selected", "true")
    expect(screen.getByRole("tab", { name: "a.conf" })).toHaveAttribute("aria-selected", "false")
  })

  it("asks before closing a tab that has unsaved changes", async () => {
    const user = userEvent.setup()
    render(<EditorDialog path={opened.path} onClose={() => {}} onSaved={() => {}} />)

    await typeIn(user, opened.path, "x")
    await user.click(screen.getByRole("button", { name: "Close a.conf" }))

    const dialog = await screen.findByRole("dialog", { name: "Close a.conf without saving?" })
    expect(dialog).toHaveTextContent("Its unsaved changes are lost.")
    await user.click(within(dialog).getByRole("button", { name: "Discard changes" }))

    await waitFor(() => expect(screen.queryByRole("tab", { name: "a.conf" })).not.toBeInTheDocument())
    expect(screen.getByText("No file is open. Choose one from the directory.")).toBeInTheDocument()
  })
})
