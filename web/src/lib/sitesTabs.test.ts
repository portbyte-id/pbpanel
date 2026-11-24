import { describe, expect, it } from "vitest"

import type { PanelApp, Site } from "@/lib/api"
import { tabFromSearch, tabForApp, tabOfSite } from "@/lib/sitesTabs"

const app = (name: string, type: PanelApp["type"], proxy_url: string) => ({ name, type, proxy_url }) as PanelApp

const site = (domain: string, routes: { path: string; target: string }[] = [], extra: Partial<Site> = {}) =>
  ({
    domain,
    aliases: [],
    kind: "proxy",
    php_version: null,
    created_at: "",
    proxy_routes: routes.map((r) => ({ ...r, strip_prefix: true })),
    ...extra,
  }) as Site

describe("tabOfSite", () => {
  const apps = [
    app("api", "node", "http://127.0.0.1:3000"),
    app("svc", "go", "http://127.0.0.1:8080"),
    app("tool", "command", "http://127.0.0.1:9000"),
  ]

  it("puts a site that passes requests to a Node.js app on the Node.js tab only", () => {
    expect(tabOfSite(site("a.test", [{ path: "/", target: "http://127.0.0.1:3000" }]), apps)).toBe("node")
  })

  it("puts a Go site and a binary site on their own tabs", () => {
    expect(tabOfSite(site("b.test", [{ path: "/", target: "http://127.0.0.1:8080" }]), apps)).toBe("go")
    expect(tabOfSite(site("c.test", [{ path: "/x", target: "http://127.0.0.1:9000" }]), apps)).toBe("binary")
  })

  it("keeps a reverse proxy to an outside server on the PHP & Proxy tab", () => {
    expect(tabOfSite(site("d.test", [{ path: "/", target: "https://example.org" }]), apps)).toBe("web")
  })

  it("keeps a PHP or static site on the PHP & Proxy tab", () => {
    expect(tabOfSite(site("e.test", [], { kind: "php", php_version: "8.4", root: "/srv/e" }), apps)).toBe("web")
  })

  it("lists a site under the first app route that matches", () => {
    const s = site("f.test", [
      { path: "/", target: "https://example.org" },
      { path: "/api", target: "http://127.0.0.1:3000" },
    ])
    expect(tabOfSite(s, apps)).toBe("node")
  })

  it("does not match an app that has no port", () => {
    expect(tabOfSite(site("g.test", [{ path: "/", target: "" }]), [app("x", "node", "")])).toBe("web")
  })
})

describe("tab helpers", () => {
  it("maps app types to tabs", () => {
    expect(tabForApp("node")).toBe("node")
    expect(tabForApp("go")).toBe("go")
    expect(tabForApp("command")).toBe("binary")
  })

  it("falls back to PHP & Proxy for a missing or unknown tab", () => {
    expect(tabFromSearch(undefined)).toBe("web")
    expect(tabFromSearch("nope")).toBe("web")
    expect(tabFromSearch("node")).toBe("node")
  })
})
