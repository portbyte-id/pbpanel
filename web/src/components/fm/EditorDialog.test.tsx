import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { EditorDialog } from "@/components/fm/EditorDialog"
import { ApiError, type FMFile } from "@/lib/api"
import { api } from "@/lib/api"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: { fmOpenFile: vi.fn(), fmSaveFile: vi.fn() },
  }
})

// CodeMirror measures text through the browser's layout engine, which jsdom
// does not have. A textarea stands in for it: the dialog's own behaviour —
// draft, base, conflict — is what these tests are about.
vi.mock("@/components/ConfigEditor", () => ({
  ConfigEditor: ({
    value,
    onChange,
    label,
  }: {
    value: string
    onChange: (value: string) => void
    label: string
  }) => (
    <textarea aria-label={label} value={value} onChange={(e) => onChange(e.target.value)} />
  ),
}))

const opened: FMFile & { language: string } = {
  path: "/var/www/a.conf",
  content: "server { }\n",
  size: 11,
  mtime: 1_700_000_000_000_000_000,
  hash: "hash-a",
  mode: 0o644,
  language: "properties",
}

/** Types into the stubbed editor, after it has the file's content. */
async function typeInEditor(user: ReturnType<typeof userEvent.setup>, text: string) {
  const editor = await screen.findByLabelText(`Contents of ${opened.path}`)
  await user.click(editor)
  await user.keyboard(text)
}

describe("EditorDialog", () => {
  beforeEach(() => {
    vi.mocked(api.fmOpenFile).mockReset()
    vi.mocked(api.fmSaveFile).mockReset()
    vi.mocked(api.fmOpenFile).mockResolvedValue(opened)
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
  })

  it("sends the base the file was opened with, and reports the save", async () => {
    vi.mocked(api.fmSaveFile).mockResolvedValue({ ...opened, content: "server { }\nx" })
    const user = userEvent.setup()
    const onSaved = vi.fn()
    render(<EditorDialog path={opened.path} onClose={() => {}} onSaved={onSaved} />)

    await typeInEditor(user, "x")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() =>
      expect(api.fmSaveFile).toHaveBeenCalledWith(opened.path, "server { }\nx", {
        size: opened.size,
        mtime: opened.mtime,
        hash: opened.hash,
      }),
    )
    expect(onSaved).toHaveBeenCalledOnce()
  })

  it("shows the disk copy and offers an overwrite on a 409 conflict", async () => {
    const onDisk: FMFile = {
      ...opened,
      content: "server { listen 8080; }\n",
      hash: "hash-b",
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

    await typeInEditor(user, "x")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Overwrite with my draft" })).toBeInTheDocument(),
    )
    expect(screen.getByRole("alert")).toHaveTextContent("changed on disk")
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument()
    expect(screen.getByLabelText("The file as it is on disk now")).toHaveValue(
      "server { listen 8080; }\n",
    )

    // Overwriting sends the disk copy's base, which is what makes it stick.
    vi.mocked(api.fmSaveFile).mockResolvedValue({ ...onDisk, content: "server { }\nx" })
    await user.click(screen.getByRole("button", { name: "Overwrite with my draft" }))
    await waitFor(() =>
      expect(api.fmSaveFile).toHaveBeenLastCalledWith(opened.path, "server { }\nx", {
        size: onDisk.size,
        mtime: onDisk.mtime,
        hash: onDisk.hash,
      }),
    )
  })

  it("explains a config-editor redirect instead of offering an overwrite", async () => {
    vi.mocked(api.fmSaveFile).mockRejectedValue(
      new ApiError(
        409,
        "use_config_editor",
        "/var/www/a.conf is under nginx.conf, which the config editor manages",
      ),
    )
    const user = userEvent.setup()
    render(<EditorDialog path={opened.path} onClose={() => {}} onSaved={() => {}} />)

    await typeInEditor(user, "x")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent("managed by the panel's config editor"),
    )
    expect(screen.queryByRole("button", { name: /Overwrite/ })).not.toBeInTheDocument()
    // The draft is kept so the fix is one edit away.
    expect(screen.getByLabelText(`Contents of ${opened.path}`)).toHaveValue("server { }\nx")
  })

  it("shows a load failure instead of an editor", async () => {
    vi.mocked(api.fmOpenFile).mockRejectedValue(new ApiError(415, "binary_file", "binary file"))
    render(<EditorDialog path={opened.path} onClose={() => {}} onSaved={() => {}} />)

    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("binary file"))
    expect(screen.queryByLabelText(/Contents of/)).not.toBeInTheDocument()
  })
})
