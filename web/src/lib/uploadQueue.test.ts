import { beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError, type FMUploadInfo } from "@/lib/api"
import { api } from "@/lib/api"
import { uploadQueue } from "@/lib/uploadQueue"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      fmUploadStart: vi.fn(),
      fmUploadChunk: vi.fn(),
      fmUploadComplete: vi.fn(),
      fmUploadCancel: vi.fn(),
    },
  }
})

const info = (over: Partial<FMUploadInfo> = {}): FMUploadInfo => ({
  id: "a".repeat(32),
  dir: "/var/www",
  name: "a.bin",
  size: 4,
  received: 0,
  overwrite: false,
  chunk_max: 4,
  ...over,
})

const fileOf = (text: string, name = "a.bin") => new File([text], name)

describe("uploadQueue", () => {
  beforeEach(() => {
    vi.mocked(api.fmUploadStart).mockReset()
    vi.mocked(api.fmUploadChunk).mockReset()
    vi.mocked(api.fmUploadComplete).mockReset()
    vi.mocked(api.fmUploadCancel).mockReset()
    for (const t of uploadQueue.getSnapshot()) uploadQueue.cancel(t.key)
    uploadQueue.clearFinished()
  })

  it("starts, sends one chunk, and completes with the whole file's hash", async () => {
    vi.mocked(api.fmUploadStart).mockResolvedValue(info({ size: 4 }))
    vi.mocked(api.fmUploadChunk).mockResolvedValue(info({ size: 4, received: 4 }))
    vi.mocked(api.fmUploadComplete).mockResolvedValue("/var/www/a.bin")

    uploadQueue.enqueue("/var/www", [fileOf("abcd")])
    await vi.waitFor(() => expect(uploadQueue.getSnapshot()[0].status).toBe("done"))

    expect(api.fmUploadStart).toHaveBeenCalledWith({
      dir: "/var/www",
      name: "a.bin",
      size: 4,
      overwrite: false,
    })
    expect(api.fmUploadChunk).toHaveBeenCalledOnce()
    const [, offset, blob, sum] = vi.mocked(api.fmUploadChunk).mock.calls[0]
    expect(offset).toBe(0)
    // jsdom's Blob lacks text(); the bytes are read through FileReader.
    const text: string = await new Promise((resolve, reject) => {
      const reader = new FileReader()
      reader.onload = () => resolve(reader.result as string)
      reader.onerror = () => reject(reader.error ?? new Error("the chunk could not be read"))
      reader.readAsText(blob)
    })
    expect(text).toBe("abcd")
    expect(sum).toMatch(/^[0-9a-f]{64}$/)
    expect(api.fmUploadComplete).toHaveBeenCalledWith("a".repeat(32), sum)
    expect(uploadQueue.getSnapshot()[0].path).toBe("/var/www/a.bin")
  })

  it("resumes from the offset the server reports on an offset_mismatch", async () => {
    vi.mocked(api.fmUploadStart).mockResolvedValue(info({ size: 8, chunk_max: 4 }))
    vi.mocked(api.fmUploadChunk)
      // The client thinks it is at 0; the server already has 4 bytes.
      .mockRejectedValueOnce(
        new ApiError(409, "offset_mismatch", "chunk offset does not match", {
          payload: {
            error: "offset_mismatch",
            detail: "chunk offset does not match",
            received: 4,
          },
        }),
      )
      .mockResolvedValueOnce(info({ size: 8, received: 8 }))
    vi.mocked(api.fmUploadComplete).mockResolvedValue("/var/www/a.bin")

    uploadQueue.enqueue("/var/www", [fileOf("abcdefgh")])
    await vi.waitFor(() => expect(uploadQueue.getSnapshot()[0].status).toBe("done"))

    expect(vi.mocked(api.fmUploadChunk).mock.calls.map((c) => c[1])).toEqual([0, 4])
  })

  it("gives a duplicate name in one drop a counter instead of overwriting", async () => {
    vi.mocked(api.fmUploadStart).mockImplementation(async (input) => info({ name: input.name }))
    vi.mocked(api.fmUploadChunk).mockResolvedValue(info({ received: 1, size: 1, chunk_max: 1 }))
    vi.mocked(api.fmUploadComplete).mockResolvedValue("/var/www/a.bin")

    uploadQueue.enqueue("/var/www", [fileOf("a", "a.bin"), fileOf("b", "a.bin")])
    await vi.waitFor(() =>
      expect(uploadQueue.getSnapshot().every((t) => t.status === "done")).toBe(true),
    )

    expect(uploadQueue.getSnapshot().map((t) => t.file.name)).toEqual(["a.bin", "a (2).bin"])
  })

  it("marks a failure with the server's sentence and can retry it", async () => {
    vi.mocked(api.fmUploadStart).mockRejectedValueOnce(
      new ApiError(409, "conflict", "/var/www/a.bin already exists"),
    )
    uploadQueue.enqueue("/var/www", [fileOf("abcd")])
    await vi.waitFor(() => expect(uploadQueue.getSnapshot()[0].status).toBe("failed"))
    expect(uploadQueue.getSnapshot()[0].error).toBe("/var/www/a.bin already exists")

    vi.mocked(api.fmUploadStart).mockResolvedValue(info({ size: 4 }))
    vi.mocked(api.fmUploadChunk).mockResolvedValue(info({ size: 4, received: 4 }))
    vi.mocked(api.fmUploadComplete).mockResolvedValue("/var/www/a.bin")

    uploadQueue.retry(uploadQueue.getSnapshot()[0].key)
    await vi.waitFor(() => expect(uploadQueue.getSnapshot()[0].status).toBe("done"))
  })

  it("keeps the task after the subscriber that started it goes away", async () => {
    // The store is module-level, so a page unmounting leaves the upload running.
    vi.mocked(api.fmUploadStart).mockResolvedValue(info({ size: 4 }))
    vi.mocked(api.fmUploadChunk).mockResolvedValue(info({ size: 4, received: 4 }))
    vi.mocked(api.fmUploadComplete).mockResolvedValue("/var/www/a.bin")

    const unsubscribe = uploadQueue.subscribe(() => {})
    uploadQueue.enqueue("/var/www", [fileOf("abcd")])
    unsubscribe()

    await vi.waitFor(() => expect(uploadQueue.getSnapshot()[0].status).toBe("done"))
    expect(uploadQueue.getSnapshot()[0].path).toBe("/var/www/a.bin")
  })
})
