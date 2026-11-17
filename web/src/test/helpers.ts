import type { Snapshot } from "@/lib/sysinfo"

/** A snapshot with every key the server always sends. */
export function snap(time: string, over: Partial<Snapshot> = {}): Snapshot {
  return {
    time,
    rates: true,
    cpu_percent: 10,
    cpu_cores: [10, 10],
    load: { one: 0.5, five: 0.4, fifteen: 0.3, running: 1, total: 400 },
    memory: {
      total_bytes: 8 * 1024 ** 3,
      available_bytes: 4 * 1024 ** 3,
      used_bytes: 4 * 1024 ** 3,
      free_bytes: 2 * 1024 ** 3,
      buffers_bytes: 0,
      cached_bytes: 0,
      swap_total_bytes: 0,
      swap_free_bytes: 0,
      swap_used_bytes: 0,
    },
    uptime_seconds: 3 * 86_400 + 4 * 3_600,
    disks: [],
    net: [],
    ...over,
  }
}

const enc = new TextEncoder()

/** A response whose body delivers `chunks` and then ends. */
export function streamOf(chunks: string[]): Response {
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const c of chunks) controller.enqueue(enc.encode(c))
      controller.close()
    },
  })
  return new Response(body, { status: 200 })
}


/** A response whose body stays open until `close`, for a followed tail. */
export function openStreamResponse() {
  let controller!: ReadableStreamDefaultController<Uint8Array>
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      controller = c
    },
  })
  return {
    response: new Response(body, { status: 200 }),
    push: (text: string) => controller.enqueue(enc.encode(text)),
    close: () => controller.close(),
  }
}
