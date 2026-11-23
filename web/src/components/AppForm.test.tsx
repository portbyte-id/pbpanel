import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, describe, expect, it, vi } from "vitest"

import { AppForm, formatEnv, nodeMajors, parseEnv, parseLines } from "@/components/AppDialog"
import { appendLog } from "@/components/AppLogs"
import { APPS_ROOT } from "@/components/PathPicker"
import { ApiError, api, type FMEntry, type FMListing, type NodeVersion, type PanelApp } from "@/lib/api"
import { domainsOf, formatUptime, isRunning, typeLabel } from "@/pages/AppsPage"
import { validNodeRequest } from "@/pages/RuntimesPage"

const node = (version: string): NodeVersion => ({ version, path: `/n/${version}`, used_by: [] })

describe("parseLines", () => {
  it("keeps one argument per line, inner spaces included, and drops blank lines", () => {
    expect(parseLines("--title\nmy app\n\n  \r\n--x\r\n")).toEqual(["--title", "my app", "--x"])
    expect(parseLines("")).toEqual([])
  })
})

describe("parseEnv", () => {
  it("reads KEY=value lines, keeps '=' inside a value and skips comments", () => {
    expect(parseEnv("A=1\n# note\n\nB=x=y\nC=").env).toEqual({ A: "1", B: "x=y", C: "" })
  })

  it("rejects a line with no key or no equals sign", () => {
    expect(parseEnv("A=1\njust text").error).toContain("just text")
    expect(parseEnv("=value").error).not.toBeNull()
  })

  it("round-trips through formatEnv in key order", () => {
    const text = formatEnv({ B: "2", A: "1" })
    expect(text).toBe("A=1\nB=2")
    expect(parseEnv(text).env).toEqual({ A: "1", B: "2" })
  })
})

describe("nodeMajors", () => {
  it("lists each installed major once, newest first, and keeps the app's own", () => {
    expect(nodeMajors([node("22.11.0"), node("22.9.0"), node("20.18.0")])).toEqual(["22", "20"])
    expect(nodeMajors([node("22.11.0")], "18")).toEqual(["22", "18"])
    expect(nodeMajors([])).toEqual([])
  })

  it("orders by version numbers and keeps an app's full stored version as it is", () => {
    expect(nodeMajors([node("20.18.0")], "22.11.0")).toEqual(["22.11.0", "20"])
    expect(nodeMajors([node("9.1.0"), node("10.0.0")])).toEqual(["10", "9"])
  })
})

describe("small helpers", () => {
  it("formats an uptime with its two largest units", () => {
    expect(formatUptime(5)).toBe("5s")
    expect(formatUptime(200)).toBe("3m 20s")
    expect(formatUptime(7500)).toBe("2h 5m")
    expect(formatUptime(90000 + 3600)).toBe("1d 2h")
  })

  it("treats STARTING as up and everything else as not", () => {
    expect(isRunning({ state: "RUNNING" })).toBe(true)
    expect(isRunning({ state: "STARTING" })).toBe(true)
    for (const s of ["STOPPED", "FATAL", "BACKOFF", "not loaded", "unknown"]) {
      expect(isRunning({ state: s })).toBe(false)
    }
  })

  it("labels the kinds", () => {
    expect(typeLabel({ type: "node", node_version: "22" })).toBe("Node.js 22")
    expect(typeLabel({ type: "go", node_version: "" })).toBe("Program")
    expect(typeLabel({ type: "command", node_version: "" })).toBe("Command")
  })

  it("accepts only a major, major.minor or major.minor.patch", () => {
    for (const ok of ["22", "22.11", "22.11.0", " 24 "]) expect(validNodeRequest(ok)).toBe(true)
    for (const bad of ["", "latest", "22;rm", "22.11.0.1", "v22", "$(id)"]) {
      expect(validNodeRequest(bad)).toBe(false)
    }
  })

  it("keeps only the tail of a long log", () => {
    const big = "x".repeat(250_000)
    expect(appendLog("", big).length).toBe(200_000)
    expect(appendLog("a\n", "b\n")).toBe("a\nb\n")
  })
})

describe("AppForm", () => {
  const entry = (parent: string, name: string, kind: FMEntry["kind"]): FMEntry => ({
    name,
    path: `${parent}/${name}`,
    kind,
    size: 0,
    mode: 0o644,
    uid: 0,
    gid: 0,
    owner: "www",
    group: "www",
    mtime: 0,
  })
  const listing = (path: string, entries: FMEntry[]): FMListing => ({
    path,
    entries,
    total: entries.length,
    offset: 0,
  })
  const appDir = `${APPS_ROOT}/web`
  const bin = `${appDir}/bin`
  const tree: Record<string, FMEntry[]> = {
    [APPS_ROOT]: [entry(APPS_ROOT, "web", "dir")],
    [appDir]: [entry(appDir, "bin", "dir"), entry(appDir, "notes.txt", "file")],
    [bin]: [entry(bin, "web-binary", "file"), entry(bin, "notes.txt", "file")],
  }

  const renderForm = (props: Partial<React.ComponentProps<typeof AppForm>> = {}) => {
    const onSubmit = vi.fn()
    render(
      <AppForm
        app={null}
        nodeVersions={[]}
        onSubmit={onSubmit}
        onCancel={() => {}}
        pending={false}
        error={null}
        {...props}
      />,
    )
    return onSubmit
  }

  it("submits a program with its arguments, environment and port", async () => {
    const user = userEvent.setup()
    const onSubmit = renderForm()

    await user.type(screen.getByLabelText("Name"), " web ")
    await user.type(screen.getByLabelText("Program path"), "/www/services/sites/web/bin/web")
    await user.type(screen.getByLabelText("Arguments"), "--listen{Enter}:3000")
    await user.type(screen.getByLabelText("Environment"), "MODE=prod{Enter}# comment{Enter}A=b")
    await user.type(screen.getByLabelText("Port"), "3000")
    await user.click(screen.getByRole("button", { name: "Create app" }))

    expect(onSubmit).toHaveBeenCalledWith({
      name: "web",
      type: "go",
      command: "/www/services/sites/web/bin/web",
      args: ["--listen", ":3000"],
      env: { MODE: "prod", A: "b" },
      user: "www",
      restart: "always",
      autostart: true,
      port: 3000,
    })
  })

  it("passes the domain as a second argument, only when one is typed", async () => {
    const user = userEvent.setup()
    const onSubmit = renderForm()

    await user.type(screen.getByLabelText("Name"), "web")
    await user.type(screen.getByLabelText("Program path"), "/www/services/sites/web/bin/web")
    await user.type(screen.getByLabelText("Port"), "3000")
    await user.type(screen.getByLabelText("Domain"), "web.test")
    await user.type(screen.getByLabelText("Other domains"), "www.web.test, b.test")
    await user.click(screen.getByRole("button", { name: "Create app" }))

    expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ name: "web", port: 3000 }), {
      domain: "web.test",
      aliases: ["www.web.test", "b.test"],
    })
  })

  it("refuses a domain when the app has no port", async () => {
    const user = userEvent.setup()
    const onSubmit = renderForm()

    await user.type(screen.getByLabelText("Name"), "web")
    await user.type(screen.getByLabelText("Program path"), "/www/services/sites/web/bin/web")
    await user.type(screen.getByLabelText("Domain"), "web.test")
    await user.click(screen.getByRole("button", { name: "Create app" }))

    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByRole("alert")).toHaveTextContent("Set a port first")
  })

  it("asks for a Node version and an entry script, and sends the version", async () => {
    const user = userEvent.setup()
    const onSubmit = renderForm({ nodeVersions: [node("22.11.0"), node("20.18.0")] })

    await user.selectOptions(screen.getByLabelText("Kind"), "node")
    expect(screen.queryByLabelText("Program path")).not.toBeInTheDocument()
    await user.type(screen.getByLabelText("Name"), "api")
    await user.type(screen.getByLabelText("Entry script"), "server.js")
    await user.click(screen.getByRole("button", { name: "Create app" }))

    expect(onSubmit).toHaveBeenCalledWith(
      expect.objectContaining({ type: "node", command: "server.js", node_version: "22", port: null }),
    )
  })

  it("cannot create a Node app while no version is installed", async () => {
    const user = userEvent.setup()
    renderForm()
    await user.selectOptions(screen.getByLabelText("Kind"), "node")
    expect(screen.getByText(/Install one on the Runtimes page first/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Create app" })).toBeDisabled()
  })

  it("refuses a bad environment line and a bad port before sending anything", async () => {
    const user = userEvent.setup()
    const onSubmit = renderForm()
    await user.type(screen.getByLabelText("Name"), "web")
    await user.type(screen.getByLabelText("Program path"), "/bin/web")

    await user.type(screen.getByLabelText("Environment"), "oops")
    await user.click(screen.getByRole("button", { name: "Create app" }))
    expect(screen.getByRole("alert")).toHaveTextContent(/oops/)

    await user.clear(screen.getByLabelText("Environment"))
    await user.type(screen.getByLabelText("Port"), "70000")
    await user.click(screen.getByRole("button", { name: "Create app" }))
    expect(screen.getByRole("alert")).toHaveTextContent(/between 1 and 65535/)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("shows a rejected field under that field, and only there", () => {
    const err = new ApiError(409, "conflict", "another app already uses that port", { field: "port" })
    renderForm({ error: err })
    const port = screen.getByLabelText("Port")
    expect(port).toHaveAttribute("aria-invalid", "true")
    expect(screen.getAllByRole("alert")).toHaveLength(1)
    expect(screen.getByRole("alert")).toHaveTextContent("another app already uses that port")
  })

  it("shows supervisord's own output when it refuses the file", () => {
    const err = new ApiError(422, "config_rejected", "supervisord rejected the program file", {
      output: "line 3: bad value",
    })
    renderForm({ error: err })
    expect(screen.getByTestId("validator-output")).toHaveTextContent("line 3: bad value")
  })

  it("edits an existing app with its name fixed and its values filled in", async () => {
    const user = userEvent.setup()
    const app: PanelApp = {
      id: 1, name: "web", type: "go", command: "/bin/web", args: ["--x"], workdir: "/srv/web",
      env: { A: "1" }, user: "www", node_version: "", autostart: false, restart: "never", port: 8080,
      created_at: "", state: "RUNNING", pid: 1, uptime_seconds: 1, exitstatus: 0, spawn_error: "",
      proxy_url: "http://127.0.0.1:8080",
    }
    const onSubmit = renderForm({ app })

    expect(screen.getByLabelText("Name")).toBeDisabled()
    expect(screen.getByLabelText("Port")).toHaveValue("8080")
    expect(screen.getByLabelText("Environment")).toHaveValue("A=1")
    await user.click(screen.getByRole("button", { name: "Save changes" }))
    expect(onSubmit).toHaveBeenCalledWith(
      expect.objectContaining({
        name: "web", args: ["--x"], workdir: "/srv/web", env: { A: "1" },
        restart: "never", autostart: false, port: 8080,
      }),
    )
  })

  it("starts a Node app saved without a version on an installed one, and sends that", async () => {
    const user = userEvent.setup()
    const app: PanelApp = {
      id: 2, name: "api", type: "node", command: "server.js", args: [], workdir: "/srv/api",
      env: {}, user: "www", node_version: "", autostart: true, restart: "always", port: 3000,
      created_at: "", state: "STOPPED", pid: 0, uptime_seconds: 0, exitstatus: 0, spawn_error: "",
      proxy_url: "",
    }
    const onSubmit = renderForm({ app, nodeVersions: [node("22.11.0"), node("20.18.0")] })

    expect(screen.getByLabelText("Node.js version")).toHaveValue("22")
    await user.click(screen.getByRole("button", { name: "Save changes" }))
    expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ type: "node", node_version: "22" }))
  })

  describe("the path browsers", () => {
    const mockTree = () =>
      vi.spyOn(api, "fmList").mockImplementation(async ({ path }) => listing(path, tree[path] ?? []))

    afterEach(() => vi.restoreAllMocks())

    it("offers folders and files for the program, and fills the field", async () => {
      const fm = mockTree()
      const user = userEvent.setup()
      const onSubmit = renderForm()

      await user.type(screen.getByLabelText("Name"), "web")
      await user.click(screen.getByRole("button", { name: "Browse… for the program" }))
      const dialog = await screen.findByRole("dialog", { name: "Choose the program" })
      // Starts in the app's own folder, which exists here.
      expect(fm).toHaveBeenCalledWith({ path: appDir, sort: "name", hidden: true })
      await user.click(within(dialog).getByRole("button", { name: "bin" }))
      await screen.findByRole("button", { name: /web-binary/ })

      // A file has to be chosen: the confirm is dead until one is.
      const use = screen.getByRole("button", { name: "Use this file" })
      expect(use).toBeDisabled()
      await user.click(screen.getByRole("button", { name: /notes\.txt/ }))
      await user.click(use)

      await waitFor(() =>
        expect(screen.queryByRole("dialog", { name: "Choose the program" })).not.toBeInTheDocument(),
      )
      expect(screen.getByLabelText("Program path")).toHaveValue(`${bin}/notes.txt`)

      await user.type(screen.getByLabelText("Port"), "3000")
      await user.click(screen.getByRole("button", { name: "Create app" }))
      expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ command: `${bin}/notes.txt` }))
    })

    it("offers folders only for the working directory", async () => {
      mockTree()
      const user = userEvent.setup()
      renderForm()

      await user.click(screen.getByRole("button", { name: "Browse… for the working directory" }))
      const dialog = await screen.findByRole("dialog", { name: "Choose the working directory" })
      await user.click(await screen.findByRole("button", { name: /web/ }))
      // A folder picker offers no files at all.
      expect(await screen.findByRole("button", { name: /bin/ })).toBeInTheDocument()
      expect(within(dialog).queryByRole("button", { name: /notes\.txt/ })).not.toBeInTheDocument()

      await user.click(screen.getByRole("button", { name: "Use this folder" }))
      await waitFor(() =>
        expect(
          screen.queryByRole("dialog", { name: "Choose the working directory" }),
        ).not.toBeInTheDocument(),
      )
      expect(screen.getByLabelText("Working directory")).toHaveValue(`${APPS_ROOT}/web`)
    })

    it("reads a chosen .env file into the Environment box", async () => {
      vi.spyOn(api, "fmList").mockImplementation(async ({ path }) =>
        listing(
          path,
          path === `${APPS_ROOT}/web`
            ? [
                entry(`${APPS_ROOT}/web`, ".env", "file"),
                entry(`${APPS_ROOT}/web`, "server.js", "file"),
              ]
            : [],
        ),
      )
      const open = vi
        .spyOn(api, "fmOpenFile")
        .mockResolvedValue({
          path: `${APPS_ROOT}/web/.env`,
          content: "MODE=prod\n# a note\nTOKEN=abc",
          size: 27,
          mtime: 0,
          hash: "h",
          mode: 0o644,
          language: "shell",
        })
      const user = userEvent.setup()
      renderForm()

      await user.type(screen.getByLabelText("Name"), "web")
      await user.click(screen.getByRole("button", { name: "Load .env file…" }))
      const dialog = await screen.findByRole("dialog", { name: "Choose a .env file" })
      // Only dotenv files are listed, and dotfiles are shown at all.
      expect(within(dialog).queryByRole("button", { name: /server\.js/ })).not.toBeInTheDocument()
      await user.click(within(dialog).getByRole("button", { name: /\.env/ }))
      await user.click(screen.getByRole("button", { name: "Use this file" }))

      await waitFor(() => expect(open).toHaveBeenCalledWith(`${APPS_ROOT}/web/.env`))
      await waitFor(() =>
        expect(screen.getByLabelText("Environment")).toHaveValue("MODE=prod\nTOKEN=abc"),
      )
      expect(screen.getByText(/Read from \/www\/services\/sites\/web\/\.env/)).toBeInTheDocument()
    })

    it("keeps the keys already typed when the file does not set them", async () => {
      vi.spyOn(api, "fmList").mockResolvedValue(
        listing(APPS_ROOT, [entry(APPS_ROOT, ".env", "file")]),
      )
      vi.spyOn(api, "fmOpenFile").mockResolvedValue({
        path: `${APPS_ROOT}/.env`,
        content: "A=file",
        size: 6,
        mtime: 0,
        hash: "h",
        mode: 0o644,
        language: "shell",
      })
      const user = userEvent.setup()
      renderForm()

      await user.type(screen.getByLabelText("Environment"), "A=mine{Enter}B=typed")
      await user.click(screen.getByRole("button", { name: "Load .env file…" }))
      await user.click(await screen.findByRole("button", { name: /\.env/ }))
      await user.click(screen.getByRole("button", { name: "Use this file" }))

      await waitFor(() =>
        expect(screen.getByLabelText("Environment")).toHaveValue("A=file\nB=typed"),
      )
    })

    it("says why a .env file could not be read and leaves the box alone", async () => {
      vi.spyOn(api, "fmList").mockResolvedValue(
        listing(APPS_ROOT, [entry(APPS_ROOT, ".env", "file")]),
      )
      vi.spyOn(api, "fmOpenFile").mockRejectedValue(
        new ApiError(415, "binary_file", "this file is not text"),
      )
      const user = userEvent.setup()
      renderForm()

      await user.type(screen.getByLabelText("Environment"), "A=1")
      await user.click(screen.getByRole("button", { name: "Load .env file…" }))
      await user.click(await screen.findByRole("button", { name: /\.env/ }))
      await user.click(screen.getByRole("button", { name: "Use this file" }))

      expect(await screen.findByRole("alert")).toHaveTextContent("this file is not text")
      expect(screen.getByLabelText("Environment")).toHaveValue("A=1")
    })
  })
})

describe("domainsOf", () => {
  const site = (domain: string, kind: "proxy" | "static", target?: string) =>
    ({ domain, aliases: [], kind, proxy_target: target, php_version: null, created_at: "" }) as never
  it("lists the proxy sites that point at the app's port", () => {
    const sites = [
      site("a.test", "proxy", "http://127.0.0.1:3000"),
      site("b.test", "proxy", "http://127.0.0.1:4000"),
      site("c.test", "static"),
    ]
    expect(domainsOf({ proxy_url: "http://127.0.0.1:3000" }, sites)).toEqual(["a.test"])
    expect(domainsOf({ proxy_url: "" }, sites)).toEqual([])
  })
})
