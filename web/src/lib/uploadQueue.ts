/**
 * The file-manager upload queue (M3 3.10).
 *
 * The queue lives outside React, in a module-level store read through
 * `useSyncExternalStore`, so an upload keeps running while the operator
 * navigates away from Files and the page still shows its progress on return.
 *
 * One upload is: `POST /api/fm/uploads`, then repeated `PUT` chunks at the
 * offset the server reports with the chunk's SHA-256 in `X-Chunk-SHA256`, then
 * `POST …/complete` with the whole file's SHA-256. An `offset_mismatch` answer
 * carries the offset the server actually has; the queue resumes from there.
 */

import { ApiError, type FMUploadInfo } from "@/lib/api"
import { api } from "@/lib/api"

export type UploadStatus =
  | "queued"
  | "hashing"
  | "uploading"
  | "completing"
  | "done"
  | "failed"
  | "canceled"

export interface UploadTask {
  /** Stable client id, the React key. */
  key: string
  dir: string
  file: File
  status: UploadStatus
  /** Bytes the server has confirmed. */
  sent: number
  uploadId: string | null
  error: string | null
  /** Where the file landed, once done. */
  path: string | null
}

type Listener = () => void

/** SHA-256 of a blob as lowercase hex. jsdom's Blob lacks `arrayBuffer()` and
 * Web Crypto, so the bytes go through FileReader and Node's implementation is
 * used when the window's is missing; a real browser has both. */
async function sha256Hex(blob: Blob): Promise<string> {
  const bytes = await new Promise<ArrayBuffer>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(reader.result as ArrayBuffer)
    reader.onerror = () => reject(reader.error ?? new Error("the file could not be read"))
    reader.readAsArrayBuffer(blob)
  })
  const subtle = globalThis.crypto?.subtle ?? (await import("node:crypto")).webcrypto.subtle
  const digest = await subtle.digest("SHA-256", bytes)
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("")
}

class UploadQueue {
  #tasks: UploadTask[] = []
  #listeners = new Set<Listener>()
  #running = false
  #nextKey = 0
  /** Overwrite answer per file name, kept from the enqueue call. */
  #overwrite = new Map<string, boolean>()

  subscribe = (listener: Listener): (() => void) => {
    this.#listeners.add(listener)
    return () => {
      this.#listeners.delete(listener)
    }
  }

  getSnapshot = (): UploadTask[] => this.#tasks

  enqueue(dir: string, files: File[], overwrite = false): void {
    const taken = new Set(
      this.#tasks
        .filter((t) => t.dir === dir && t.status !== "done" && t.status !== "canceled")
        .map((t) => t.file.name),
    )
    const added: UploadTask[] = []
    for (const file of files) {
      let name = file.name
      let n = 1
      while (taken.has(name)) {
        const dot = file.name.lastIndexOf(".")
        const stem = dot > 0 ? file.name.slice(0, dot) : file.name
        const ext = dot > 0 ? file.name.slice(dot) : ""
        n += 1
        name = `${stem} (${n})${ext}`
      }
      taken.add(name)
      const renamed = name === file.name ? file : new File([file], name, { type: file.type })
      added.push({
        key: `u${this.#nextKey++}`,
        dir,
        file: renamed,
        status: "queued",
        sent: 0,
        uploadId: null,
        error: null,
        path: null,
      })
      this.#overwrite.set(name, overwrite)
    }
    this.#tasks = [...this.#tasks, ...added]
    this.#emit()
    void this.#drain()
  }

  retry(key: string): void {
    this.#patch(key, { status: "queued", error: null })
    void this.#drain()
  }

  cancel(key: string): void {
    const task = this.#tasks.find((t) => t.key === key)
    if (!task || task.status === "done" || task.status === "canceled") return
    this.#patch(key, { status: "canceled" })
    if (task.uploadId) {
      void api.fmUploadCancel(task.uploadId).catch(() => {
        // A cancel the server refuses leaves a partial file for the sweeper.
      })
    }
  }

  /** Removes rows that reached a terminal state. */
  clearFinished(): void {
    this.#tasks = this.#tasks.filter((t) => t.status !== "done" && t.status !== "canceled")
    this.#emit()
  }

  #patch(key: string, next: Partial<UploadTask>): void {
    this.#tasks = this.#tasks.map((t) => (t.key === key ? { ...t, ...next } : t))
    this.#emit()
  }

  #emit(): void {
    for (const l of this.#listeners) l()
  }

  /** Runs queued tasks one at a time: the panel's disk is the shared resource. */
  async #drain(): Promise<void> {
    if (this.#running) return
    this.#running = true
    try {
      for (;;) {
        const task = this.#tasks.find((t) => t.status === "queued")
        if (!task) break
        await this.#run(task)
      }
    } finally {
      this.#running = false
    }
  }

  async #run(task: UploadTask): Promise<void> {
    const { key, file, dir } = task
    try {
      const whole = await sha256Hex(file)
      if (this.#status(key) !== "queued") return
      this.#patch(key, { status: "uploading" })

      const info = await api.fmUploadStart({
        dir,
        name: file.name,
        size: file.size,
        overwrite: this.#overwrite.get(file.name) ?? false,
      })
      this.#patch(key, { uploadId: info.id, sent: info.received })

      let state: FMUploadInfo = info
      const chunkMax = info.chunk_max > 0 ? info.chunk_max : 8 << 20
      while (state.received < file.size) {
        if (this.#status(key) !== "uploading") return
        const end = Math.min(state.received + chunkMax, file.size)
        const blob = file.slice(state.received, end)
        const sum = await sha256Hex(blob)
        try {
          state = await api.fmUploadChunk(info.id, state.received, blob, sum)
        } catch (err) {
          if (err instanceof ApiError && err.code === "offset_mismatch") {
            // The server is behind: resume from the offset it reports.
            const received = err.payload.received
            if (typeof received === "number" && Number.isFinite(received) && received >= 0) {
              state = { ...state, received }
              this.#patch(key, { sent: received })
              continue
            }
          }
          throw err
        }
        this.#patch(key, { sent: state.received })
      }

      this.#patch(key, { status: "completing" })
      const path = await api.fmUploadComplete(info.id, whole)
      this.#patch(key, { status: "done", path, sent: file.size })
    } catch (err) {
      const message =
        err instanceof ApiError
          ? err.detail
          : err instanceof Error
            ? err.message
            : "The upload failed."
      this.#patch(key, { status: "failed", error: message })
    }
  }

  #status(key: string): UploadStatus | null {
    return this.#tasks.find((t) => t.key === key)?.status ?? null
  }
}

/** The one queue the app uses. It outlives any page that renders it. */
export const uploadQueue = new UploadQueue()
