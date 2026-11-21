import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { describe, expect, it, vi } from "vitest"

import { ProxyRouteEditor, routesFromDrafts } from "@/components/CreateSiteDialog"
import { ApiError } from "@/lib/api"

/**
 * The route editor is one component used twice: in the create dialog and in the
 * site's Reverse proxy section. These tests pin the behaviour both rely on —
 * add, remove, toggle, drop an empty row, and show the server's refusal.
 */
describe("routesFromDrafts", () => {
  it("drops an entirely empty row and trims what is left", () => {
    expect(
      routesFromDrafts([
        { path: " /api ", target: " http://127.0.0.1:8080 ", strip_prefix: true },
        { path: "", target: "", strip_prefix: true },
        { path: "/", target: "http://127.0.0.1:3000", strip_prefix: false },
      ]),
    ).toEqual([
      { path: "/api", target: "http://127.0.0.1:8080", strip_prefix: true },
      { path: "/", target: "http://127.0.0.1:3000", strip_prefix: false },
    ])
  })

  it("keeps a half-filled row so the server can say what is wrong with it", () => {
    expect(routesFromDrafts([{ path: "/api", target: "", strip_prefix: true }])).toEqual([
      { path: "/api", target: "", strip_prefix: true },
    ])
  })
})

describe("ProxyRouteEditor", () => {
  const renderEditor = (error: unknown = null) => {
    const onChange = vi.fn()
    render(
      <ProxyRouteEditor
        routes={[{ path: "/api", target: "http://127.0.0.1:8080", strip_prefix: true }]}
        onChange={onChange}
        error={error}
      />,
    )
    return onChange
  }

  it("adds and removes a row", async () => {
    const user = userEvent.setup()
    const onChange = renderEditor()
    await user.click(screen.getByRole("button", { name: "Add route" }))
    expect(onChange).toHaveBeenLastCalledWith([
      { path: "/api", target: "http://127.0.0.1:8080", strip_prefix: true },
      { path: "", target: "", strip_prefix: true },
    ])
    await user.click(screen.getByRole("button", { name: "Remove route /api" }))
    expect(onChange).toHaveBeenLastCalledWith([])
  })

  it("edits the path", async () => {
    const user = userEvent.setup()
    // A stateful wrapper, as the caller is: the editor is controlled, so a static
    // prop would snap back and make typing append to the old value.
    const onChange = vi.fn()
    function Harness() {
      const [routes, setRoutes] = useState([{ path: "/api", target: "http://127.0.0.1:8080", strip_prefix: true }])
      return (
        <ProxyRouteEditor
          routes={routes}
          onChange={(r) => {
            onChange(r)
            setRoutes(r)
          }}
        />
      )
    }
    render(<Harness />)
    await user.clear(screen.getByLabelText("Path"))
    await user.type(screen.getByLabelText("Path"), "/v2")
    expect(screen.getByLabelText("Path")).toHaveValue("/v2")
    expect(onChange).toHaveBeenLastCalledWith([
      { path: "/v2", target: "http://127.0.0.1:8080", strip_prefix: true },
    ])
  })

  it("toggles the prefix", async () => {
    const user = userEvent.setup()
    const onChange = renderEditor()
    await user.click(screen.getByLabelText(/Strip the prefix/))
    expect(onChange).toHaveBeenLastCalledWith([
      { path: "/api", target: "http://127.0.0.1:8080", strip_prefix: false },
    ])
  })

  it("says there is nothing to proxy when the list is empty", () => {
    render(<ProxyRouteEditor routes={[]} onChange={() => {}} />)
    expect(screen.getByText(/No routes yet/)).toBeInTheDocument()
    expect(screen.queryByLabelText("Path")).not.toBeInTheDocument()
  })

  it("shows a server refusal for the list, without the field's own name", () => {
    renderEditor(
      new ApiError(
        422,
        "invalid_input",
        'proxy_routes: / cannot be proxied while the site runs PHP: both would claim "location /"',
        { field: "proxy_routes" },
      ),
    )
    const editor = screen.getByTestId("proxy-route-editor")
    const alert = within(editor).getByRole("alert")
    expect(alert).toHaveTextContent("both would claim")
    expect(alert).not.toHaveTextContent("proxy_routes:")
  })

  it("ignores an error about another field", () => {
    renderEditor(new ApiError(422, "invalid_input", "domain: nope", { field: "domain" }))
    expect(
      within(screen.getByTestId("proxy-route-editor")).queryByRole("alert"),
    ).not.toBeInTheDocument()
  })
})
