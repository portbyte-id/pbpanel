import { useEffect, useState, useSyncExternalStore } from "react"

import { Button } from "@/components/ui/button"
import { ApiError, type FMEntry, type FMListing } from "@/lib/api"
import { uploadQueue, type UploadTask } from "@/lib/uploadQueue"
import { cn } from "@/lib/utils"

/**
 * Bytes as the operator reads them: one decimal place below 10, whole above,
 * and a dash for a folder or an unknown size.
 */
export function formatSize(bytes: number, isDirLike: boolean): string {
  if (isDirLike || bytes < 0) return "—"
  if (bytes < 1024) return `${bytes} B`
  const units = ["KiB", "MiB", "GiB", "TiB"]
  let value = bytes
  let unit = "B"
  for (const next of units) {
    if (value < 1024) break
    value /= 1024
    unit = next
  }
  const rounded = value < 10 ? Math.round(value * 10) / 10 : Math.round(value)
  return `${rounded} ${unit}`
}

/** Mode as the operator knows it from `ls`: the low twelve bits, octal. */
export function formatMode(mode: number): string {
  return (mode & 0o7777).toString(8).padStart(3, "0")
}

/** Unix nanoseconds as a local date-time, in the listing's column. */
export function formatMtime(ns: number): string {
  return new Date(ns / 1_000_000).toLocaleString()
}

/**
 * Splits an absolute path into breadcrumb links. The root is its own crumb, so
 * `/var/www` renders as `/`, `var`, `www`.
 */
export function crumbs(path: string): { label: string; path: string }[] {
  const out = [{ label: "/", path: "/" }]
  let acc = ""
  for (const part of path.split("/").filter((p) => p !== "")) {
    acc += `/${part}`
    out.push({ label: part, path: acc })
  }
  return out
}

/** Sort keys the toolbar offers, matching the API's `sort` parameter. */
export type FMSort = "name" | "size" | "mtime"

export interface FMSelection {
  /** The selected rows, in listing order. */
  entries: FMEntry[]
  /** True when every visible row is selected. */
  all: boolean
  toggle(entry: FMEntry, shift: boolean): void
  toggleAll(): void
  clear(): void
}

/**
 * Multi-select over one page of a listing. A click toggles one row; a
 * shift-click takes the range from the last row clicked.
 */
export function useFMSelection(listing: FMListing | undefined): FMSelection {
  const [selected, setSelected] = useState<Map<string, FMEntry>>(new Map())
  const [anchor, setAnchor] = useState<string | null>(null)

  // A new directory empties the selection: the old rows are not on screen.
  useEffect(() => {
    setSelected(new Map())
  }, [listing?.path])

  const entries = listing?.entries ?? []
  const selectedEntries = entries.filter((e) => selected.has(e.path))
  const all = entries.length > 0 && selectedEntries.length === entries.length

  return {
    entries: selectedEntries,
    all,
    toggle(entry, shift) {
      setSelected((prev) => {
        const next = new Map(prev)
        if (shift && anchor !== null) {
          const from = entries.findIndex((e) => e.path === anchor)
          const to = entries.findIndex((e) => e.path === entry.path)
          if (from !== -1 && to !== -1) {
            next.clear()
            const [lo, hi] = from < to ? [from, to] : [to, from]
            for (let i = lo; i <= hi; i++) next.set(entries[i].path, entries[i])
            return next
          }
        }
        setAnchor(entry.path)
        if (next.has(entry.path)) next.delete(entry.path)
        else next.set(entry.path, entry)
        return next
      })
    },
    toggleAll() {
      setSelected(all ? new Map() : new Map(entries.map((e) => [e.path, e])))
    },
    clear() {
      setSelected(new Map())
    },
  }
}

/** The app-level upload queue, read so a page renders it wherever it is. */
export function useUploadQueue(): UploadTask[] {
  return useSyncExternalStore(uploadQueue.subscribe, uploadQueue.getSnapshot)
}

/** A short human phrase for one queue row's state. */
export function uploadStateLine(task: UploadTask): string {
  switch (task.status) {
    case "queued":
      return "Waiting…"
    case "hashing":
      return "Hashing…"
    case "completing":
      return "Finishing…"
    case "done":
      return "Done"
    case "canceled":
      return "Canceled"
    case "failed":
      return task.error ?? "Failed"
    case "uploading": {
      const pct = task.file.size > 0 ? Math.round((task.sent / task.file.size) * 100) : 100
      return `${pct}%`
    }
  }
}

/**
 * The breadcrumbs of the listing: `/`, then each part as a link, the last one
 * plain text.
 */
export function FMBreadcrumbs({
  path,
  onNavigate,
}: {
  path: string
  onNavigate: (path: string) => void
}) {
  const parts = crumbs(path)
  return (
    <nav aria-label="Breadcrumb" className="flex min-w-0 flex-wrap items-center gap-1 text-sm">
      {parts.map((c, i) => (
        <span key={c.path} className="flex items-center gap-1">
          {i > 0 && (
            <span aria-hidden="true" className="text-muted-foreground">
              /
            </span>
          )}
          {i === parts.length - 1 ? (
            <span className="font-medium" aria-current="location">
              {c.label}
            </span>
          ) : (
            <button
              type="button"
              className="text-muted-foreground hover:text-foreground underline-offset-4 hover:underline"
              onClick={() => onNavigate(c.path)}
            >
              {c.label}
            </button>
          )}
        </span>
      ))}
    </nav>
  )
}

/**
 * The upload panel under the listing. The queue survives navigation because it
 * lives in `lib/uploadQueue`; this only renders it.
 */
export function UploadQueuePanel({
  className,
  onQuiet,
}: {
  className?: string
  /** Called when the queue goes from active to quiet, so the page can refresh. */
  onQuiet?: () => void
}) {
  const tasks = useUploadQueue()
  const active = tasks.some((t) => t.status !== "done" && t.status !== "canceled")

  useEffect(() => {
    if (!active) onQuiet?.()
    // Fire once per transition to quiet, not on every render while quiet.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active])

  if (tasks.length === 0) return null

  return (
    <section aria-label="Uploads" className={cn("rounded-lg border p-3", className)}>
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold">Uploads</h2>
        <Button size="sm" variant="ghost" onClick={() => uploadQueue.clearFinished()}>
          Clear finished
        </Button>
      </div>
      <ul className="divide-border mt-1 flex flex-col divide-y">
        {tasks.map((t) => {
          const running =
            t.status === "queued" ||
            t.status === "hashing" ||
            t.status === "uploading" ||
            t.status === "completing"
          return (
            <li key={t.key} className="flex items-center gap-3 py-1.5 text-sm">
              <span className="min-w-0 flex-1 truncate font-mono text-xs">
                {t.dir}/{t.file.name}
              </span>
              <span
                className={cn(
                  "text-xs",
                  t.status === "failed" && "text-destructive",
                  (t.status === "done" || t.status === "canceled") && "text-muted-foreground",
                )}
              >
                {uploadStateLine(t)}
              </span>
              {running && (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => uploadQueue.cancel(t.key)}
                  aria-label={`Cancel upload of ${t.file.name}`}
                >
                  Cancel
                </Button>
              )}
              {t.status === "failed" && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => uploadQueue.retry(t.key)}
                  aria-label={`Retry upload of ${t.file.name}`}
                >
                  Retry
                </Button>
              )}
            </li>
          )
        })}
      </ul>
    </section>
  )
}

/** A sentence for the error a file-manager request produced. */
export function fmErrorText(err: unknown, fallback: string): string {
  return err instanceof ApiError ? err.detail : fallback
}
