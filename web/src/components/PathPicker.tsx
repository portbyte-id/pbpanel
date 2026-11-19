import { useEffect, useRef, useState, type ReactNode } from "react"
import { ArrowUp, File as FileIcon, Folder } from "lucide-react"

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
import { cn } from "@/lib/utils"

/** The folder apps are created under: where their browser opens by default. */
export const APPS_ROOT = "/www/services/apps"

/** The folder a path sits in; "/" for a path at the root. */
export function parentOf(path: string): string {
  const i = path.lastIndexOf("/")
  return i <= 0 ? "/" : path.slice(0, i)
}

/** Whether a file may be chosen. Every file is offered when this is left out. */
export type NameFilter = (name: string) => boolean

/** A dotenv file: `.env`, `.env.local`, and any other name ending in `.env`. */
export const isEnvFile: NameFilter = (name) =>
  name === ".env" || name.startsWith(".env.") || name.endsWith(".env")

/** What a pick chooses: the folder being viewed, or a file inside it. */
export type PickMode = "dir" | "file"

function isDir(entry: FMEntry): boolean {
  return entry.kind === "dir" || (entry.kind === "symlink" && entry.target_kind === "dir")
}

/** Whether a path is one of the roots or inside one. "/" holds everything. */
function insideAnyRoot(path: string, roots: string[]): boolean {
  return roots.some((r) => path === r || path.startsWith(r === "/" ? "/" : `${r}/`))
}

/** The root a path sits in: the first one that holds it, or the first root. */
function baseOf(path: string, roots: string[]): string {
  return roots.find((r) => insideAnyRoot(path, [r])) ?? roots[0] ?? "/"
}

/**
 * The browser, without the dialog around it. It starts at `start` when that is
 * inside a root (else the first root) and never goes above the root it is in; the
 * chips switch between the roots. A file picker lists files too, shows dotfiles
 * and asks for one to be selected; a folder picker offers folders only. Mounted
 * fresh on every open, so it never reopens in an old place.
 */
function PathBrowser({
  start,
  roots,
  mode,
  filter,
  errorText,
  onPick,
  onCancel,
}: {
  start?: string
  roots: string[]
  mode: PickMode
  filter?: NameFilter
  /** A caller's own message for a folder that cannot be listed. */
  errorText?: (path: string) => string | null
  onPick: (path: string) => void
  onCancel: () => void
}) {
  const [path, setPath] = useState(() =>
    start && insideAnyRoot(start, roots) ? start : baseOf(start ?? "", roots),
  )
  const base = baseOf(path, roots)
  const [entries, setEntries] = useState<FMEntry[] | null>(null)
  const [picked, setPicked] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  // Read through a ref: the caller passes a fresh function on every render, and
  // an effect that depended on it would list the folder again and again.
  const errorTextRef = useRef(errorText)
  errorTextRef.current = errorText

  useEffect(() => {
    let live = true
    setEntries(null)
    setPicked(null)
    // A file picker always shows dotfiles, because `.env` is one and is the file
    // most often wanted. A folder picker keeps the file manager's default.
    const list =
      mode === "file"
        ? api.fmList({ path, sort: "name", hidden: true })
        : api.fmList({ path, sort: "name" })
    list
      .then((l) => {
        if (!live) return
        setEntries(l.entries)
        setError(null)
      })
      .catch((err: unknown) => {
        if (!live) return
        setEntries([])
        setError(
          errorTextRef.current?.(path) ??
            (err instanceof Error ? err.message : "The folder could not be listed."),
        )
      })
    return () => {
      live = false
    }
  }, [path, mode])

  const atBase = path === base
  const dirs = (entries ?? []).filter(isDir)
  const files =
    mode === "file" ? (entries ?? []).filter((e) => !isDir(e) && (filter?.(e.name) ?? true)) : []
  const shown = [...dirs, ...files]

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
          <ArrowUp aria-hidden="true" />
        </Button>
        <code className="min-w-0 flex-1 truncate text-xs" title={path}>
          {path}
        </code>
      </div>

      <ul
        className="border-input max-h-64 min-h-32 overflow-y-auto rounded-md border p-1"
        aria-busy={entries === null}
      >
        {entries === null && <li className="text-muted-foreground px-2 py-1.5 text-sm">Loading…</li>}
        {entries !== null && shown.length === 0 && !error && (
          <li className="text-muted-foreground px-2 py-1.5 text-sm">
            {mode === "file" ? "No folders and no matching files here." : "No sub-folders here."}
          </li>
        )}
        {shown.map((e) => {
          const dir = isDir(e)
          return (
            <li key={e.path}>
              <button
                type="button"
                aria-pressed={dir ? undefined : picked === e.path}
                onClick={() => (dir ? setPath(e.path) : setPicked(e.path))}
                className={cn(
                  "hover:bg-accent flex min-h-9 w-full items-center gap-2 rounded px-2 text-left text-sm",
                  !dir && picked === e.path && "bg-accent",
                )}
              >
                <span aria-hidden>{dir ? <Folder className="size-3.5" /> : <FileIcon className="size-3.5" />}</span>
                <span className="truncate">{e.name}</span>
              </button>
            </li>
          )
        })}
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
        <Button
          type="button"
          disabled={mode === "file" && picked === null}
          onClick={() => onPick(mode === "file" ? (picked ?? path) : path)}
        >
          {mode === "file" ? "Use this file" : "Use this folder"}
        </Button>
      </DialogFooter>
    </>
  )
}

/**
 * The picker dialog: a folder, or a file inside one. `roots` limits where it may
 * go and defaults to the whole filesystem; it is null while the caller is still
 * reading them. It closes itself before reporting the choice.
 */
export function PathPicker({
  open,
  onOpenChange,
  start,
  roots = ["/"],
  mode = "dir",
  filter,
  title,
  description,
  errorText,
  onPick,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The folder to open on. Outside every root, the first root is used. */
  start?: string
  /** The folders the browser may go in. The default is the whole filesystem. */
  roots?: string[] | null
  mode?: PickMode
  /** The files `mode="file"` offers. Folders are always offered. */
  filter?: NameFilter
  title?: string
  description?: ReactNode
  /** A caller's own message for a folder that cannot be listed. */
  errorText?: (path: string) => string | null
  onPick: (path: string) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {title ?? (mode === "file" ? "Choose a file" : "Choose a folder")}
          </DialogTitle>
          <DialogDescription>
            {description ??
              (mode === "file"
                ? "Open a folder, then choose a file in it."
                : "Open a folder, then choose it.")}
          </DialogDescription>
        </DialogHeader>
        {roots === null ? (
          <p className="text-muted-foreground py-6 text-center text-sm">Loading the folders…</p>
        ) : (
          <PathBrowser
            start={start}
            roots={roots}
            mode={mode}
            filter={filter}
            errorText={errorText}
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
