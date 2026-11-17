import { describe, expect, it, vi } from "vitest"

import { ApiClient, ApiError, CSRF_HEADER, isSafeMethod } from "@/lib/api"

/** A fetch double that records the request and answers with `response`. */
function stubFetch(response: Response) {
  const calls: { url: string; init: RequestInit }[] = []
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init: init ?? {} })
    return response
  })
  return { fetch: fn as unknown as typeof globalThis.fetch, calls }
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function headerOf(call: { init: RequestInit }, name: string): string | null {
  const headers = new Headers(call.init.headers)
  return headers.get(name)
}

describe("CSRF header", () => {
  it("is sent on every unsafe method once a token is known", async () => {
    const cases = [
      { method: "POST", path: "/api/logout" },
      { method: "PUT", path: "/api/settings" },
      { method: "DELETE", path: "/api/anything" },
    ]

    for (const { method, path } of cases) {
      const { fetch, calls } = stubFetch(new Response(null, { status: 204 }))
      const client = new ApiClient({ fetch })
      client.setCsrfToken("token-123")

      await client.request(path, { method })

      expect(calls).toHaveLength(1)
      expect(headerOf(calls[0], CSRF_HEADER)).toBe("token-123")
    }
  })

  it("is not sent on safe methods, which the server exempts", async () => {
    for (const method of ["GET", "HEAD", "OPTIONS"]) {
      const { fetch, calls } = stubFetch(jsonResponse({ ok: true }))
      const client = new ApiClient({ fetch })
      client.setCsrfToken("token-123")

      await client.request("/api/me", { method })

      expect(headerOf(calls[0], CSRF_HEADER)).toBeNull()
    }
  })

  it("is not sent when no token is known yet", async () => {
    const { fetch, calls } = stubFetch(new Response(null, { status: 204 }))
    const client = new ApiClient({ fetch })

    await client.request("/api/logout", { method: "POST" })

    expect(headerOf(calls[0], CSRF_HEADER)).toBeNull()
  })

  it("is captured from the login response and dropped on logout", async () => {
    const login = stubFetch(jsonResponse({ username: "admin", csrf_token: "from-login" }))
    const client = new ApiClient({ fetch: login.fetch })

    await client.login("admin", "pw")
    expect(client.csrfToken).toBe("from-login")

    const logout = stubFetch(new Response(null, { status: 204 }))
    const second = new ApiClient({ fetch: logout.fetch })
    second.setCsrfToken("from-login")
    await second.logout()
    expect(second.csrfToken).toBeNull()
    // The token was still sent: logout is unsafe and needs the header.
    expect(headerOf(logout.calls[0], CSRF_HEADER)).toBe("from-login")
  })
})

describe("401 handling", () => {
  it("calls the unauthorized handler on a protected route", async () => {
    const { fetch } = stubFetch(
      jsonResponse({ error: "unauthorized", detail: "log in to use this endpoint" }, 401),
    )
    const onUnauthorized = vi.fn()
    const client = new ApiClient({ fetch, onUnauthorized })

    await expect(client.settings()).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it("does not call it on a failed login, which is a wrong password", async () => {
    const { fetch } = stubFetch(
      jsonResponse(
        { error: "invalid_credentials", detail: "username or password is incorrect" },
        401,
      ),
    )
    const onUnauthorized = vi.fn()
    const client = new ApiClient({ fetch, onUnauthorized })

    await expect(client.login("admin", "wrong")).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it("does not call it for a non-401 failure", async () => {
    const { fetch } = stubFetch(jsonResponse({ error: "internal_error", detail: "boom" }, 500))
    const onUnauthorized = vi.fn()
    const client = new ApiClient({ fetch, onUnauthorized })

    await expect(client.audit()).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).not.toHaveBeenCalled()
  })
})

describe("error envelope", () => {
  it("becomes an ApiError carrying the code, the detail and the status", async () => {
    const { fetch } = stubFetch(
      jsonResponse({ error: "invalid_setting", detail: "port must be between 1 and 65535" }, 400),
    )
    const client = new ApiClient({ fetch })

    const error = await client.settings().catch((e: unknown) => e)

    expect(error).toBeInstanceOf(ApiError)
    const apiError = error as ApiError
    expect(apiError.code).toBe("invalid_setting")
    expect(apiError.detail).toBe("port must be between 1 and 65535")
    expect(apiError.status).toBe(400)
    expect(apiError.message).toBe("port must be between 1 and 65535")
    expect(apiError.isUnauthorized).toBe(false)
  })

  it("falls back to a synthetic code when the body is not the envelope", async () => {
    const { fetch } = stubFetch(new Response("<html>gateway error</html>", { status: 502 }))
    const client = new ApiClient({ fetch })

    const error = (await client.audit().catch((e: unknown) => e)) as ApiError

    expect(error).toBeInstanceOf(ApiError)
    expect(error.code).toBe("http_502")
    expect(error.status).toBe(502)
  })

  it("is not applied to a successful response", async () => {
    const { fetch } = stubFetch(jsonResponse({ groups: [] }))
    const client = new ApiClient({ fetch })

    await expect(client.settings()).resolves.toEqual({ groups: [] })
  })
})

describe("request bodies", () => {
  it("sends JSON with the right content type", async () => {
    const { fetch, calls } = stubFetch(jsonResponse({ groups: [] }))
    const client = new ApiClient({ fetch })
    client.setCsrfToken("t")

    await client.saveSettings([{ name: "panel.port", value: "9443" }])

    expect(calls[0].init.method).toBe("PUT")
    expect(headerOf(calls[0], "Content-Type")).toBe("application/json")
    expect(JSON.parse(String(calls[0].init.body))).toEqual({
      settings: [{ name: "panel.port", value: "9443" }],
    })
  })

  it("resolves a 204 to undefined", async () => {
    const { fetch } = stubFetch(new Response(null, { status: 204 }))
    const client = new ApiClient({ fetch })

    await expect(client.request("/api/password", { method: "POST" })).resolves.toBeUndefined()
  })
})

describe("isSafeMethod", () => {
  it("matches the server's rule", () => {
    expect(isSafeMethod("GET")).toBe(true)
    expect(isSafeMethod("get")).toBe(true)
    expect(isSafeMethod("HEAD")).toBe(true)
    expect(isSafeMethod("OPTIONS")).toBe(true)
    expect(isSafeMethod("POST")).toBe(false)
    expect(isSafeMethod("PUT")).toBe(false)
    expect(isSafeMethod("PATCH")).toBe(false)
    expect(isSafeMethod("DELETE")).toBe(false)
  })
})

describe("TLS routes", () => {
  it("encodes the domain and sends the three upload fields as JSON", async () => {
    const { fetch, calls } = stubFetch(jsonResponse({ cert: { domain: "a.test" }, warnings: [] }))
    const client = new ApiClient({ fetch })
    client.setCsrfToken("t")

    await client.uploadSiteCert("a.test", { certificate: "c", chain: "", private_key: "k" })

    expect(calls[0].url).toBe("/api/sites/a.test/tls/upload")
    expect(calls[0].init.method).toBe("POST")
    expect(JSON.parse(String(calls[0].init.body))).toEqual({
      certificate: "c",
      chain: "",
      private_key: "k",
    })
  })

  it("returns the job id of an issuance", async () => {
    const { fetch, calls } = stubFetch(jsonResponse({ job_id: "j1" }, 202))
    const id = await new ApiClient({ fetch }).issueSiteCert("a.test", {
      method: "dns01",
      names: ["*.a.test"],
      provider: "cf",
    })
    expect(id).toBe("j1")
    expect(JSON.parse(String(calls[0].init.body))).toEqual({
      method: "dns01",
      names: ["*.a.test"],
      provider: "cf",
    })
  })

  it("reads a missing certificate as null", async () => {
    const { fetch } = stubFetch(jsonResponse({ cert: null }))
    expect(await new ApiClient({ fetch }).siteTLS("a.test")).toBeNull()
  })

  it("puts a provider's token under credentials.api_token and reads nothing back", async () => {
    const { fetch, calls } = stubFetch(new Response(null, { status: 204 }))
    await new ApiClient({ fetch }).saveTLSProvider("cf", "cloudflare", "tok")
    expect(calls[0].url).toBe("/api/tls/providers/cf")
    expect(calls[0].init.method).toBe("PUT")
    expect(JSON.parse(String(calls[0].init.body))).toEqual({
      kind: "cloudflare",
      credentials: { api_token: "tok" },
    })
  })

  it("sends the redirect flag and removes a certificate with DELETE", async () => {
    const { fetch, calls } = stubFetch(jsonResponse({ domain: "a.test", redirect_http: true }))
    const client = new ApiClient({ fetch })
    await client.setSiteRedirect("a.test", true)
    expect(calls[0].url).toBe("/api/sites/a.test/tls/redirect")
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ redirect_http: true })

    const del = stubFetch(new Response(null, { status: 204 }))
    await new ApiClient({ fetch: del.fetch }).removeSiteCert("a.test")
    expect(del.calls[0].init.method).toBe("DELETE")
  })
})

describe("PHP image calls", () => {
  const job = { id: "job-1", kind: "php.build", state: "queued" }

  it("sends a pull without a body unless extensions are given, as before", async () => {
    const plain = stubFetch(jsonResponse({ job }, 202))
    await new ApiClient({ fetch: plain.fetch }).pullPHP("8.3")
    expect(plain.calls[0].url).toBe("/api/php/8.3/pull")
    expect(plain.calls[0].init.body).toBeUndefined()

    const chosen = stubFetch(jsonResponse({ job }, 202))
    await new ApiClient({ fetch: chosen.fetch }).pullPHP("8.3", ["intl"])
    expect(JSON.parse(String(chosen.calls[0].init.body))).toEqual({ extensions: ["intl"] })
  })

  it("posts the full extension list to build, even when it is empty", async () => {
    const { fetch, calls } = stubFetch(jsonResponse({ job }, 202))
    const returned = await new ApiClient({ fetch }).buildPHP("8.3", [])

    expect(calls[0].url).toBe("/api/php/8.3/build")
    expect(calls[0].init.method).toBe("POST")
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ extensions: [] })
    expect(returned).toEqual(job)
  })

  it("surfaces a 409 from remove with the server's detail", async () => {
    const { fetch } = stubFetch(
      jsonResponse({ error: "conflict", detail: "site a.test uses PHP 8.3" }, 409),
    )
    const err = await new ApiClient({ fetch }).removePHP("8.3").catch((e: unknown) => e)

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(409)
    expect((err as ApiError).detail).toBe("site a.test uses PHP 8.3")
  })
})
