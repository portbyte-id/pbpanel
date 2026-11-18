import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { CreateSiteForm, parseAliases } from "@/components/CreateSiteDialog"
import { SiteControls } from "@/components/SiteControls"
import { ApiError, type SiteDetail } from "@/lib/api"
import { describeContainer } from "@/pages/NginxPage"

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
  const renderForm = (error: unknown = null) => {
    const onSubmit = vi.fn()
    render(<CreateSiteForm onSubmit={onSubmit} onCancel={() => {}} pending={false} error={error} />)
    return onSubmit
  }

  it("submits a static site with only the fields that apply", async () => {
    const user = userEvent.setup()
    const onSubmit = renderForm()

    expect(screen.queryByLabelText("Pass requests to")).not.toBeInTheDocument()
    await user.type(screen.getByLabelText("Domain"), " a.test ")
    await user.type(screen.getByLabelText("Other domains"), "www.a.test, b.test")
    await user.click(screen.getByRole("button", { name: "Create site" }))

    expect(onSubmit).toHaveBeenCalledWith({
      domain: "a.test",
      aliases: ["www.a.test", "b.test"],
      kind: "static",
    })
  })

  it("swaps the folder for the proxy target when the kind changes", async () => {
    const user = userEvent.setup()
    const onSubmit = renderForm()

    await user.click(screen.getByRole("radio", { name: /An app or another server/ }))
    expect(screen.queryByLabelText("Folder")).not.toBeInTheDocument()

    await user.type(screen.getByLabelText("Domain"), "api.test")
    await user.type(screen.getByLabelText("Pass requests to"), "http://127.0.0.1:3000")
    await user.click(screen.getByRole("button", { name: "Create site" }))

    expect(onSubmit).toHaveBeenCalledWith({
      domain: "api.test",
      aliases: [],
      kind: "proxy",
      proxy_target: "http://127.0.0.1:3000",
    })
  })

  it("cannot be submitted without a domain", () => {
    renderForm()
    expect(screen.getByRole("button", { name: "Create site" })).toBeDisabled()
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
  it("saves only the field that was changed", async () => {
    const user = userEvent.setup()
    const onSave = vi.fn()
    render(<SiteControls detail={detail()} onSave={onSave} pending={false} error={null} />)

    expect(screen.getByRole("button", { name: "Save target" })).toBeDisabled()

    const target = screen.getByLabelText("Pass requests to")
    await user.clear(target)
    await user.type(target, "http://127.0.0.1:4000")
    await user.click(screen.getByRole("button", { name: "Save target" }))
    expect(onSave).toHaveBeenCalledWith({ proxy_target: "http://127.0.0.1:4000" })

    const aliases = screen.getByLabelText("Other domains")
    await user.clear(aliases)
    await user.click(screen.getByRole("button", { name: "Save domains" }))
    expect(onSave).toHaveBeenLastCalledWith({ aliases: [] })
  })

  it("disables a detached control and says why, next to it", () => {
    const d = detail()
    const reason =
      "the site file has no `include /www/panel/nginx/includes/api.test/proxy.conf;` line; restore it or regenerate the site file"
    d.features[3] = { ...d.features[3], attached: false, reason }
    render(<SiteControls detail={d} onSave={() => {}} pending={false} error={null} />)

    const target = screen.getByLabelText("Pass requests to")
    expect(target).toBeDisabled()
    expect(screen.getByRole("button", { name: "Save target" })).toBeDisabled()
    const help = document.getElementById(target.getAttribute("aria-describedby") ?? "")
    expect(help).toHaveTextContent(reason)

    // The control that is still connected keeps working.
    expect(screen.getByLabelText("Other domains")).toBeEnabled()

    const row = document.querySelector('[data-feature="proxy"]') as HTMLElement
    expect(within(row).getByText("detached")).toBeInTheDocument()
    const names = document.querySelector('[data-feature="names"]') as HTMLElement
    expect(within(names).getByText("connected")).toBeInTheDocument()
  })

  it("has no proxy control on a static site", () => {
    const d = detail({ kind: "static", proxy_target: undefined, root: "/www/services/sites/a.test" })
    d.features = d.features.slice(0, 3)
    render(<SiteControls detail={d} onSave={() => {}} pending={false} error={null} />)
    expect(screen.queryByLabelText("Pass requests to")).not.toBeInTheDocument()
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

describe("describeContainer", () => {
  it("covers running, stopped, missing and Docker not answering", () => {
    expect(describeContainer({ exists: true, running: true, status: "running" })).toMatchObject({
      badge: "running",
      ok: true,
    })
    expect(describeContainer({ exists: true, running: false, status: "exited" })).toMatchObject({
      badge: "exited",
      ok: false,
    })
    expect(describeContainer({ exists: false, running: false, status: "missing" })).toMatchObject({
      badge: "not created",
      ok: false,
    })
    expect(
      describeContainer({
        exists: false,
        running: false,
        status: "",
        error: "Docker did not answer",
      }),
    ).toMatchObject({ badge: "unknown", ok: false, text: "Docker did not answer" })
  })
})
