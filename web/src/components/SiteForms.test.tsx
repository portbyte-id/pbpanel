import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { CreateSiteForm, parseAliases } from "@/components/CreateSiteDialog"
import { SiteControls } from "@/components/SiteControls"
import { ApiError, type SiteDetail } from "@/lib/api"
import { describeService } from "@/pages/NginxPage"

describe("parseAliases", () => {
  it("splits on spaces, commas and newlines and drops blanks", () => {
    expect(parseAliases(" www.a.test, b.test\n c.test ,, ")).toEqual([
      "www.a.test",
      "b.test",
      "c.test",
    ])
    expect(parseAliases("   ")).toEqual([])
  })
})

describe("CreateSiteForm", () => {
  const renderForm = (
    error: unknown = null,
    props: { phpVersions?: string[]; initial?: { target: string } } = {},
  ) => {
    const onSubmit = vi.fn()
    render(
      <CreateSiteForm
        onSubmit={onSubmit}
        onCancel={() => {}}
        pending={false}
        error={error}
        phpVersions={props.phpVersions ?? []}
        initial={props.initial}
      />,
    )
    return onSubmit
  }

  it("submits a site with neither PHP nor routes", async () => {
    const user = userEvent.setup()
    const onSubmit = renderForm()

    await user.type(screen.getByLabelText("Domain"), " a.test ")
    await user.type(screen.getByLabelText("Other domains"), "www.a.test, b.test")
    await user.click(screen.getByRole("button", { name: "Create site" }))

    expect(onSubmit).toHaveBeenCalledWith({
      domain: "a.test",
      aliases: ["www.a.test", "b.test"],
      kind: "static",
    })
  })

  it("requires a PHP version only when PHP is switched on", async () => {
    const user = userEvent.setup()
    const onSubmit = renderForm(null, { phpVersions: ["8.3", "8.4"] })

    // Off by default: no version picker, and the create is not blocked by it.
    expect(screen.queryByLabelText("PHP version")).not.toBeInTheDocument()
    await user.type(screen.getByLabelText("Domain"), "a.test")
    expect(screen.getByRole("button", { name: "Create site" })).toBeEnabled()

    await user.click(screen.getByRole("checkbox", { name: /Run PHP on this site/ }))
    expect(screen.getByLabelText("PHP version")).toHaveValue("8.4")
    await user.click(screen.getByRole("button", { name: "Create site" }))
    expect(onSubmit).toHaveBeenCalledWith({
      domain: "a.test",
      aliases: [],
      kind: "php",
      php_version: "8.4",
    })
  })

  it("says how to get PHP when switching it on with none installed", async () => {
    const user = userEvent.setup()
    renderForm(null, { phpVersions: [] })
    await user.type(screen.getByLabelText("Domain"), "a.test")
    await user.click(screen.getByRole("checkbox", { name: /Run PHP on this site/ }))
    expect(screen.getByText(/No PHP version is installed yet/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Create site" })).toBeDisabled()
  })

  it("submits PHP and a proxy route in one site", async () => {
    const user = userEvent.setup()
    const onSubmit = renderForm(null, { phpVersions: ["8.3"] })

    await user.type(screen.getByLabelText("Domain"), "app.test")
    await user.click(screen.getByRole("checkbox", { name: /Run PHP on this site/ }))
    await user.click(screen.getByRole("button", { name: "Add route" }))
    await user.type(screen.getByLabelText("Path"), "/api")
    await user.type(screen.getByLabelText("Pass requests to"), "http://127.0.0.1:8080")
    await user.click(screen.getByRole("button", { name: "Create site" }))

    expect(onSubmit).toHaveBeenCalledWith({
      domain: "app.test",
      aliases: [],
      kind: "php",
      php_version: "8.3",
      proxy_routes: [{ path: "/api", target: "http://127.0.0.1:8080", strip_prefix: true }],
    })
  })

  it("creates a pure proxy site when the folder is off", async () => {
    const user = userEvent.setup()
    const onSubmit = renderForm()

    await user.type(screen.getByLabelText("Domain"), "api.test")
    await user.click(screen.getByRole("checkbox", { name: /Serve files from a folder/ }))
    await user.click(screen.getByRole("button", { name: "Add route" }))
    await user.type(screen.getByLabelText("Path"), "/")
    await user.type(screen.getByLabelText("Pass requests to"), "http://127.0.0.1:3000")
    await user.click(screen.getByLabelText(/Strip the prefix/))
    await user.click(screen.getByRole("button", { name: "Create site" }))

    expect(onSubmit).toHaveBeenCalledWith({
      domain: "api.test",
      aliases: [],
      kind: "proxy",
      proxy_routes: [{ path: "/", target: "http://127.0.0.1:3000", strip_prefix: false }],
    })
  })

  it("seeds a route from an app's proxy link as a subpath", async () => {
    const user = userEvent.setup()
    const onSubmit = renderForm(null, { initial: { target: "http://127.0.0.1:3011" } })
    // A seeded route is a subpath, not "/", which would fight the folder.
    expect(screen.getByLabelText("Path")).toHaveValue("/api")
    expect(screen.getByLabelText("Pass requests to")).toHaveValue("http://127.0.0.1:3011")
    await user.type(screen.getByLabelText("Domain"), "a.test")
    await user.click(screen.getByRole("button", { name: "Create site" }))
    expect(onSubmit).toHaveBeenCalledWith({
      domain: "a.test",
      aliases: [],
      kind: "static",
      proxy_routes: [{ path: "/api", target: "http://127.0.0.1:3011", strip_prefix: true }],
    })
  })

  it("cannot be submitted without a domain", () => {
    renderForm()
    expect(screen.getByRole("button", { name: "Create site" })).toBeDisabled()
  })

  it("shows a rejected route under the route list", async () => {
    const user = userEvent.setup()
    renderForm(
      new ApiError(
        422,
        "invalid_input",
        'proxy_routes: / cannot be proxied while the site runs PHP: both would claim "location /"',
        { field: "proxy_routes" },
      ),
    )
    await user.type(screen.getByLabelText("Domain"), "a.test")
    await user.click(screen.getByRole("button", { name: "Add route" }))
    await user.type(screen.getByLabelText("Path"), "/")
    await user.type(screen.getByLabelText("Pass requests to"), "http://127.0.0.1:3000")

    const editor = screen.getByTestId("proxy-route-editor")
    const alert = within(editor).getByRole("alert")
    expect(alert).toHaveTextContent("both would claim")
    // The field's own name is stripped: the sentence reads as prose.
    expect(alert).not.toHaveTextContent("proxy_routes:")
  })

  it("shows a rejected field under that field", () => {
    renderForm(
      new ApiError(422, "invalid_input", "domain: wildcards are not supported yet", {
        field: "domain",
      }),
    )
    const input = screen.getByLabelText("Domain")
    expect(input).toHaveAttribute("aria-invalid", "true")
    const alert = screen.getByRole("alert")
    expect(alert).toHaveTextContent("wildcards are not supported yet")
    expect(input.getAttribute("aria-describedby")).toContain(alert.id)
  })

  it("shows nginx's output verbatim when the generated config is rejected", () => {
    const output = 'nginx: [emerg] unknown directive "bogus" in /www/panel/nginx/sites/a.test.conf:3'
    renderForm(
      new ApiError(
        422,
        "config_rejected",
        "nginx rejected the configuration; the previous files are back in place",
        { output: `${output}\n` },
      ),
    )
    expect(screen.getByRole("alert")).toHaveTextContent("the previous files are back in place")
    expect(screen.getByTestId("validator-output")).toHaveTextContent(output)
  })
})

function detail(overrides: Partial<SiteDetail> = {}): SiteDetail {
  const inc = (name: string) => `/www/panel/nginx/includes/api.test/${name}.conf`
  return {
    domain: "api.test",
    aliases: ["www.api.test"],
    kind: "proxy",
    proxy_target: "http://127.0.0.1:3000",
    php_version: null,
    created_at: "2026-11-17T12:00:00Z",
    site_file: "/www/panel/nginx/sites/api.test.conf",
    site_file_missing: false,
    features: [
      { feature: "listen", include: inc("listen"), attached: true },
      { feature: "names", include: inc("names"), attached: true },
      { feature: "acme", include: inc("acme"), attached: true },
      { feature: "proxy", include: inc("proxy"), attached: true },
    ],
    ...overrides,
  }
}

describe("SiteControls", () => {
  it("saves the domains and nothing else", async () => {
    const user = userEvent.setup()
    const onSave = vi.fn()
    render(<SiteControls detail={detail()} onSave={onSave} pending={false} error={null} />)

    // The routes live in their own section now; this control writes only names.
    expect(screen.queryByLabelText("Pass requests to")).not.toBeInTheDocument()

    const aliases = screen.getByLabelText("Other domains")
    await user.clear(aliases)
    await user.click(screen.getByRole("button", { name: "Save domains" }))
    expect(onSave).toHaveBeenCalledWith({ aliases: [] })
  })

  it("disables a detached domains control and says why, next to it", () => {
    const d = detail()
    const reason = "the site file has no `include /www/panel/nginx/includes/api.test/names.conf;` line; restore it or regenerate the site file"
    d.features[1] = { ...d.features[1], attached: false, reason }
    render(<SiteControls detail={d} onSave={() => {}} pending={false} error={null} />)

    const aliases = screen.getByLabelText("Other domains")
    expect(aliases).toBeDisabled()
    expect(screen.getByRole("button", { name: "Save domains" })).toBeDisabled()
    const help = document.getElementById(aliases.getAttribute("aria-describedby") ?? "")
    expect(help).toHaveTextContent(reason)

    const row = document.querySelector('[data-feature="names"]') as HTMLElement
    expect(within(row).getByText("detached")).toBeInTheDocument()
  })

  it("lists every feature the site file keeps, connected or not", () => {
    render(<SiteControls detail={detail()} onSave={() => {}} pending={false} error={null} />)
    for (const name of ["listen", "names", "acme", "proxy"]) {
      const row = document.querySelector(`[data-feature="${name}"]`) as HTMLElement
      expect(within(row).getByText("connected")).toBeInTheDocument()
    }
  })

  it("shows the server's refusal", () => {
    render(
      <SiteControls
        detail={detail()}
        onSave={() => {}}
        pending={false}
        error={new ApiError(409, "conflict", "the domain is already in use: b.test (used by b.test)")}
      />,
    )
    expect(screen.getByRole("alert")).toHaveTextContent("already in use")
  })
})

describe("describeService", () => {
  it("covers running, stopped, not installed and systemd not answering", () => {
    expect(describeService({ exists: true, running: true, status: "running" })).toMatchObject({
      badge: "running",
      ok: true,
    })
    expect(describeService({ exists: true, running: false, status: "inactive" })).toMatchObject({
      badge: "inactive",
      ok: false,
      text: "nginx is not running.",
    })
    expect(describeService({ exists: false, running: false, status: "missing" })).toMatchObject({
      badge: "not installed",
      ok: false,
    })
    expect(
      describeService({
        exists: false,
        running: false,
        status: "",
        error: "systemd did not answer; see the daemon log",
      }),
    ).toMatchObject({
      badge: "unknown",
      ok: false,
      text: "systemd did not answer; see the daemon log",
    })
  })
})
