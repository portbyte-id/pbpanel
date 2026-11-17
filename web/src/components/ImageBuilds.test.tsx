import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { BuildHistory, ExtensionPicker, MAX_EXTENSIONS, validatePeclName } from "@/components/ImageBuilds"
import type { ImageBuild, PHPExtension } from "@/lib/api"

const build = (over: Partial<ImageBuild> = {}): ImageBuild => ({
  id: "20261117175601-a1b2c3",
  kind: "php",
  version: "8.3",
  tag: "panel/php:8.3-x",
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

const catalogue: PHPExtension[] = [
  {
    name: "intl",
    module: "intl",
    description: "Internationalisation (ICU)",
    kind: "bundled",
    packages: ["libicu-dev"],
  },
  { name: "redis", module: "redis", description: "Redis client", kind: "pecl", packages: [] },
]

describe("validatePeclName", () => {
  it("accepts the names the server accepts", () => {
    expect(validatePeclName("redis")).toBeNull()
    expect(validatePeclName("igbinary_2")).toBeNull()
    expect(validatePeclName("ab")).toBeNull()
    expect(validatePeclName("a" + "b".repeat(39))).toBeNull()
  })

  it.each([
    ["Redis", "uppercase"],
    ["a b", "a space"],
    ["x;rm", "a shell character"],
    ["1redis", "a leading digit"],
    ["a", "a single character"],
    ["a" + "b".repeat(40), "41 characters"],
    ["", "nothing"],
  ])("rejects %j (%s)", (name) => {
    expect(validatePeclName(name)).not.toBeNull()
  })
})

describe("BuildHistory", () => {
  it("offers Activate only on a ready build that is not the one in use", async () => {
    const user = userEvent.setup()
    const onActivate = vi.fn()
    const builds = [
      build({ id: "b3", state: "ready", current: true }),
      build({ id: "b2", state: "ready", previous: true, extensions: ["intl"] }),
      build({ id: "b1", state: "failed", output: "E: unable to fetch\n" }),
      build({ id: "b0", state: "building" }),
    ]
    render(
      <BuildHistory
        builds={builds}
        emptyText="No builds yet."
        onActivate={onActivate}
        error={null}
        fallback="The build could not be activated."
      />,
    )

    const rows = screen.getAllByTestId("build-row")
    expect(within(rows[0]).queryByRole("button", { name: /Activate/ })).toBeNull()
    expect(within(rows[0]).getByText("serving")).toBeInTheDocument()
    expect(within(rows[2]).queryByRole("button", { name: /Activate/ })).toBeNull()
    expect(rows[2]).toHaveTextContent("E: unable to fetch")
    expect(within(rows[3]).queryByRole("button", { name: /Activate/ })).toBeNull()

    const activate = within(rows[1]).getByRole("button", { name: "Activate build b2" })
    await user.click(activate)
    expect(onActivate).toHaveBeenCalledWith(builds[1])
    expect(screen.getAllByRole("button", { name: /Activate/ })).toHaveLength(1)
  })

  it("shows nothing to activate when no handler is given, and the empty text when there are no builds", () => {
    const { unmount } = render(
      <BuildHistory builds={[build({ state: "ready" })]} emptyText="none" error={null} fallback="x" />,
    )
    expect(screen.queryByRole("button", { name: /Activate/ })).toBeNull()
    unmount()

    render(<BuildHistory builds={[]} emptyText="No image builds yet." error={null} fallback="x" />)
    expect(screen.getByText("No image builds yet.")).toBeInTheDocument()
  })
})

describe("ExtensionPicker", () => {
  const renderPicker = (initial: string[] = [], onSubmit = vi.fn()) => {
    render(
      <ExtensionPicker
        catalogue={catalogue}
        initial={initial}
        submitLabel="Start rebuild"
        pendingLabel="Starting…"
        pending={false}
        error={null}
        onSubmit={onSubmit}
        onCancel={() => {}}
      />,
    )
    return onSubmit
  }

  it("refuses an invalid PECL name inline and adds a valid one", async () => {
    const user = userEvent.setup()
    const onSubmit = renderPicker()
    const box = screen.getByLabelText("Another PECL package")

    await user.type(box, "Redis")
    await user.click(screen.getByRole("button", { name: "Add" }))
    expect(screen.getByRole("alert")).toHaveTextContent("Use 2 to 40 lowercase letters")
    expect(box).toHaveAttribute("aria-invalid", "true")

    await user.clear(box)
    await user.type(box, "igbinary")
    await user.click(screen.getByRole("button", { name: "Add" }))
    expect(screen.queryByRole("alert")).toBeNull()
    expect(screen.getByRole("checkbox", { name: /^igbinary/ })).toBeChecked()

    await user.click(screen.getByRole("button", { name: "Start rebuild" }))
    expect(onSubmit).toHaveBeenCalledWith(["igbinary"])
  })

  it("adds a package on Enter without submitting the form", async () => {
    const user = userEvent.setup()
    const onSubmit = renderPicker()
    await user.type(screen.getByLabelText("Another PECL package"), "redis{enter}")

    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByRole("checkbox", { name: /^redis/ })).toBeChecked()
  })

  it("keeps the current build's extensions ticked and stops at the cap", async () => {
    const user = userEvent.setup()
    const full = Array.from({ length: MAX_EXTENSIONS }, (_, i) => `ext${String(i).padStart(2, "0")}`)
    const onSubmit = renderPicker(["intl", ...full.slice(1)])

    expect(screen.getByRole("checkbox", { name: /^intl/ })).toBeChecked()
    expect(screen.getByText(`${MAX_EXTENSIONS} of ${MAX_EXTENSIONS} chosen.`)).toBeInTheDocument()

    const box = screen.getByLabelText("Another PECL package")
    await user.type(box, "redis")
    await user.click(screen.getByRole("button", { name: "Add" }))
    expect(screen.getByRole("alert")).toHaveTextContent(`At most ${MAX_EXTENSIONS} extensions`)

    await user.click(screen.getByRole("button", { name: "Start rebuild" }))
    expect(onSubmit).toHaveBeenCalledWith([...new Set(["intl", ...full.slice(1)])].sort())
  })
})
