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

/** The tree every site may live in: the default, and always offered. */
export const SITES_ROOT = "/www/services/sites"

/** The Settings key that lists more folders a site may use (see internal/webroots). */
export const WEB_ROOTS_KEY = "sites.web_roots"

/** The folders a site may be in: the sites tree, then each folder on the allowlist. */
export function parseRoots(value: string): string[] {
  const extra = value
    .split(/[,\r\n]/)
    .map((s) => s.trim())
    .filter((s) => s !== "")
  return [SITES_ROOT, ...extra]
}

function parentOf(path: string): string {
  const i = path.lastIndexOf("/")
  return i <= 0 ? "/" : path.slice(0, i)
}

/** Whether a path is one of the roots or inside one. */
function insideAnyRoot(path: string, roots: string[]): boolean {
  return roots.some((r) => path === r || path.startsWith(`${r}/`))
}

/** The root a path sits in: the first one that holds it, or the sites tree. */
function baseOf(path: string, roots: string[]): string {
  return roots.find((r) => path === r || path.startsWith(`${r}/`)) ?? SITES_ROOT
}

/**
 * The folder browser, without the dialog around it. It lists sub-folders only,
 * starts at `start` when that is inside a root (else the sites root) and calls
 * `onPick` with the folder being viewed when the user confirms. It never goes
 * above the root it is in; the chips switch between the roots. Mounted fresh on
 * every open, so it never reopens in an old place.
 */
function FolderBrowser({
  start,
  roots,
  onPick,
  onCancel,
}: {
  start?: string
  roots: string[]
  onPick: (path: string) => void
  onCancel: () => void
}) {
  const [path, setPath] = useState(() =>
    start && insideAnyRoot(start, roots) ? start : SITES_ROOT,
  )
  const base = baseOf(path, roots)
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

  const atBase = path === base

  return (
    <>
      {roots.length > 1 && (
        <div className="flex flex-wrap gap-1.5" role="group" aria-label="Roots">
          {roots.map((r) => (
            <Button
              key={r}
              type="button"
              size="sm"
              variant={r === base ? "secondary" : "outline"}
              aria-pressed={r === base}
              onClick={() => setPath(r)}
            >
              {r}
            </Button>
          ))}
        </div>
      )}
      <div className="flex items-center gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={atBase}
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

/**
 * A dialog for choosing a site folder, opened from the Folder field's Browse
 * button. The roots are the sites tree plus the folders the Settings page allows.
 * They are read each time the dialog opens; if they cannot be read, the sites
 * tree alone is offered.
 */
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
  const [roots, setRoots] = useState<string[] | null>(null)

  useEffect(() => {
    if (!open) return
    let live = true
    setRoots(null)
    api
      .settings()
      .then((res) => {
        if (!live) return
        const value =
          res.groups.flatMap((g) => g.keys).find((k) => k.name === WEB_ROOTS_KEY)?.value ?? ""
        setRoots(parseRoots(value))
      })
      .catch(() => {
        if (live) setRoots([SITES_ROOT])
      })
    return () => {
      live = false
    }
  }, [open])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Choose a folder</DialogTitle>
          <DialogDescription>
            Open a folder, then choose it. It must be inside{" "}
            {(roots ?? [SITES_ROOT]).join(" or ")}.
          </DialogDescription>
        </DialogHeader>
        {roots === null ? (
          <p className="text-muted-foreground py-6 text-center text-sm">Loading the folders…</p>
        ) : (
          <FolderBrowser
            start={start}
            roots={roots}
            onCancel={() => onOpenChange(false)}
            onPick={(p) => {
              onPick(p)
              onOpenChange(false)
            }}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}
