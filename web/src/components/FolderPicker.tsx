import { useEffect, useState } from "react"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { api, type FMEntry } from "@/lib/api"

/** The only tree sites may live in; the picker never offers to go above it. */
export const SITES_ROOT = "/www/services/sites"

function parentOf(path: string): string {
  const i = path.lastIndexOf("/")
  return i <= 0 ? "/" : path.slice(0, i)
}

/**
 * The folder browser, without the dialog around it. It lists sub-folders only,
 * starts at `start` (or the sites root) and calls `onPick` with the folder being
 * viewed when the user confirms. Mounted fresh on every open, so it never
 * reopens in an old place.
 */
function FolderBrowser({
  start,
  onPick,
  onCancel,
}: {
  start?: string
  onPick: (path: string) => void
  onCancel: () => void
}) {
  const [path, setPath] = useState(start && start.startsWith(SITES_ROOT) ? start : SITES_ROOT)
  const [dirs, setDirs] = useState<FMEntry[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let live = true
    setDirs(null)
    api
      .fmList({ path, sort: "name" })
      .then((l) => {
        if (!live) return
        setDirs(l.entries.filter((e) => e.kind === "dir"))
        setError(null)
      })
      .catch((err: unknown) => {
        if (!live) return
        setDirs([])
        setError(
          path === SITES_ROOT
            ? `${SITES_ROOT} does not exist yet. Close this and type a folder name instead; it will be created.`
            : err instanceof Error
              ? err.message
              : "The folder could not be listed.",
        )
      })
    return () => {
      live = false
    }
  }, [path])

  const atRoot = path === SITES_ROOT

  return (
    <>
      <div className="flex items-center gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={atRoot}
          onClick={() => setPath(parentOf(path))}
          aria-label="Up one folder"
        >
          ↑
        </Button>
        <code className="min-w-0 flex-1 truncate text-xs" title={path}>
          {path}
        </code>
      </div>

      <ul
        className="border-input max-h-64 min-h-32 overflow-y-auto rounded-md border p-1"
        aria-busy={dirs === null}
      >
        {dirs === null && <li className="text-muted-foreground px-2 py-1.5 text-sm">Loading…</li>}
        {dirs !== null && dirs.length === 0 && !error && (
          <li className="text-muted-foreground px-2 py-1.5 text-sm">No sub-folders here.</li>
        )}
        {dirs?.map((d) => (
          <li key={d.path}>
            <button
              type="button"
              onClick={() => setPath(d.path)}
              className="hover:bg-accent flex min-h-9 w-full items-center gap-2 rounded px-2 text-left text-sm"
            >
              <span aria-hidden>📁</span>
              <span className="truncate">{d.name}</span>
            </button>
          </li>
        ))}
      </ul>
      {error && (
        <p role="status" className="text-muted-foreground text-xs">
          {error}
        </p>
      )}

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="button" onClick={() => onPick(path)}>
          Use this folder
        </Button>
      </DialogFooter>
    </>
  )
}

/** A dialog for choosing a site folder, opened from the Folder field's Browse button. */
export function FolderPicker({
  open,
  onOpenChange,
  start,
  onPick,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  start?: string
  onPick: (path: string) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Choose a folder</DialogTitle>
          <DialogDescription>
            Open a folder, then choose it. It must be inside {SITES_ROOT}.
          </DialogDescription>
        </DialogHeader>
        <FolderBrowser
          start={start}
          onCancel={() => onOpenChange(false)}
          onPick={(p) => {
            onPick(p)
            onOpenChange(false)
          }}
        />
      </DialogContent>
    </Dialog>
  )
}
