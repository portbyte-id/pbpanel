import { describe, expect, it } from "vitest"

import { ApiClient } from "@/lib/api"
import {
  createNotifyChannel,
  markNotifyRead,
  notifyEvents,
  openNotifyStream,
  type ChannelInput,
  type NotifyEvent,
} from "@/lib/notify"

interface Call {
  url: string
  method: string
  body: string | undefined
}

function fakeClient(reply: unknown = []) {
  const calls: Call[] = []
  const client = new ApiClient({
    fetch: async (input: URL | RequestInfo, init?: RequestInit) => {
      calls.push({ url: String(input), method: init?.method ?? "GET", body: init?.body as string | undefined })
      return new Response(JSON.stringify(reply), { status: 200, headers: { "Content-Type": "application/json" } })
    },
  })
  return { client, calls }
}

const channelInput: ChannelInput = {
  name: "ops",
  type: "webhook",
  enabled: true,
  min_severity: "warning",
  kinds: [],
  quiet_start: "",
  quiet_end: "",
  send_resolved: true,
  config: { url: "https://hooks.example.com/x" },
}

describe("notify client", () => {
  it("builds the events query from the filters it is given", async () => {
    const { client, calls } = fakeClient()
    await notifyEvents({ limit: 10, unread: true }, client)
    await notifyEvents({}, client)
    expect(calls[0].url).toBe("/api/notify/events?limit=10&unread=1")
    expect(calls[1].url).toBe("/api/notify/events")
  })

  it("marks every event read with the all flag, and the given ids otherwise", async () => {
    const { client, calls } = fakeClient({ updated: 3 })
    await markNotifyRead("all", client)
    await markNotifyRead([4, 5], client)
    expect(calls[0].method).toBe("POST")
    expect(JSON.parse(calls[0].body ?? "")).toEqual({ all: true })
    expect(JSON.parse(calls[1].body ?? "")).toEqual({ ids: [4, 5] })
  })

  it("omits a secret the operator did not type, so the stored value is kept", async () => {
    const { client, calls } = fakeClient({ id: 1 })
    await createNotifyChannel(channelInput, client)
    const sent = JSON.parse(calls[0].body ?? "")
    expect(calls[0].url).toBe("/api/notify/channels")
    expect(sent).not.toHaveProperty("secret")
    expect(sent.config.url).toBe("https://hooks.example.com/x")
  })

  it("passes a typed secret through as given", async () => {
    const { client, calls } = fakeClient({ id: 1 })
    await createNotifyChannel({ ...channelInput, secret: { hmac_secret: "s3cret" } }, client)
    expect(JSON.parse(calls[0].body ?? "").secret).toEqual({ hmac_secret: "s3cret" })
  })
})

class FakeSource {
  static last: FakeSource | null = null
  onopen: (() => void) | null = null
  closed = false
  private listeners: Record<string, Array<(e: Event) => void>> = {}
  constructor(public url: string) {
    FakeSource.last = this
  }
  addEventListener(name: string, fn: (e: Event) => void) {
    ;(this.listeners[name] ??= []).push(fn)
  }
  emit(name: string, data: string) {
    for (const fn of this.listeners[name] ?? []) fn({ data } as unknown as Event)
  }
  close() {
    this.closed = true
  }
}

describe("notify stream", () => {
  it("delivers each notification and skips a malformed frame", () => {
    const got: NotifyEvent[] = []
    const close = openNotifyStream(
      (ev) => got.push(ev),
      undefined,
      (url) => new FakeSource(url) as unknown as EventSource,
    )
    const src = FakeSource.last as FakeSource
    expect(src.url).toBe("/api/notify/stream")

    src.emit("notification", JSON.stringify({ id: 7, kind: "cron.failed", subject: "job failed" }))
    src.emit("notification", "{not json")
    expect(got.map((e) => e.id)).toEqual([7])

    close()
    expect(src.closed).toBe(true)
  })

  it("reports the open so the caller can refetch after a reconnect", () => {
    let opened = 0
    openNotifyStream(
      () => undefined,
      () => opened++,
      (url) => new FakeSource(url) as unknown as EventSource,
    )
    ;(FakeSource.last as FakeSource).onopen?.()
    expect(opened).toBe(1)
  })
})
