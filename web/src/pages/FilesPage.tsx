import {
  Archive,
  ArrowDownAZ,
  ArrowUp,
  ArrowUpAZ,
  File as FileIcon,
  FilePlus,
  Folder,
  FolderPlus,
  Link2,
  MoreHorizontal,
  Pencil,
  RefreshCw,
  Trash2,
  Upload,
} from "lucide-react"
import { useCallback, useEffect, useRef, useState, type CSSProperties } from "react"

import { ConfirmDialog, ExtractDialog, NameDialog, type PromptKind } from "@/components/fm/FMDialogs"
import {
  FMBreadcrumbs,
  UploadQueuePanel,
  fmErrorText,
  formatMtime,
  formatMode,
  formatSize,
  useFMSelection,
  type FMSort,
} from "@/components/fm/FMShared"
import { EditorDialog } from "@/components/fm/EditorDialog"
import { TrashPanel } from "@/components/fm/TrashPanel"
import { JobsDrawer } from "@/components/JobsDrawer"
import { Button } from "@/components/ui/button"
import { Select } from "@/components/ui/select"
import { api, ApiError, type FMEntry, type FMListing } from "@/lib/api"
import { uploadQueue } from "@/lib/uploadQueue"
import { cn } from "@/lib/utils"

/**
 * The file manager (M3 3.10).
 *
 * Tabs switch between the listing and the trash; the listing is one page of the
 * server's answer with the sort order and hidden-file toggle the API takes.
 * Destructive actions go through typed confirmation, and anything the server
 * answers 202 for — copy, cross-mount move, archive, extract — opens the job
 * drawer, because those run as jobs and report through it.
 */
export function FilesPage() {
  const [tab, setTab] = useState<"files" | "trash">("files")
  const [path, setPath] = useState("/")
  const [hidden, setHidden] = useState(false)
  const [sort, setSort] = useState<FMSort>("name")
  const [desc, setDesc] = useState(false)
  const [listing, setListing] = useState<FMListing | null>(null)
  const [pending, setPending] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [prompt, setPrompt] = useState<PromptKind | null>(null)
  const [editing, setEditing] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<FMEntry[] | null>(null)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [menuFor, setMenuFor] = useState<string | null>(null)
  const [extracting, setExtracting] = useState<FMEntry | null>(null)
  const [extractError, setExtractError] = useState<string | null>(null)
  const [jobId, setJobId] = useState<string | null>(null)

  const selection = useFMSelection(listing ?? undefined)

  // Drag-and-drop upload: files dropped on the listing enqueue into the folder
  // being viewed, exactly as the Upload button does. `dragDepth` counts enter and
  // leave events from child rows so the highlight does not flicker when the
  // pointer crosses one.
  const [dragging, setDragging] = useState(false)
  const dragDepth = useRef(0)
  const dragProps = {
    onDragEnter: (e: React.DragEvent) => {
      if (!e.dataTransfer.types.includes("Files")) return
      e.preventDefault()
      dragDepth.current += 1
      setDragging(true)
    },
    onDragOver: (e: React.DragEvent) => {
      if (!e.dataTransfer.types.includes("Files")) return
      // Without preventDefault the browser opens the file instead of dropping it.
      e.preventDefault()
      e.dataTransfer.dropEffect = "copy"
    },
    onDragLeave: () => {
      dragDepth.current = Math.max(0, dragDepth.current - 1)
      if (dragDepth.current === 0) setDragging(false)
    },
    onDrop: (e: React.DragEvent) => {
      const files = [...e.dataTransfer.files]
      dragDepth.current = 0
      setDragging(false)
      if (files.length === 0 || listing === null) return
      e.preventDefault()
      uploadQueue.enqueue(listing.path, files)
    },
  }

  const reload = useCallback(() => {
    setPending(true)
    api
      .fmList({ path, hidden, sort, desc })
      .then((l) => {
        setListing(l)
        setError(null)
      })
      .catch((err) => {
        setError(fmErrorText(err, "The folder could not be listed."))
        setListing(null)
      })
      .finally(() => setPending(false))
  }, [path, hidden, sort, desc])

  useEffect(() => {
    reload()
  }, [reload])

  const open = (entry: FMEntry) => {
    if (entry.kind === "dir") {
      setPath(entry.path)
    } else if (entry.kind === "symlink" && entry.target_kind === "dir") {
      setPath(entry.target ?? entry.path)
    } else if (entry.kind === "file") {
      setEditing(entry.path)
    }
  }

  const deleteSelected = async () => {
    if (deleting === null) return
    setDeleteBusy(true)
    setDeleteError(null)
    try {
      // A folder needs its name typed; files need none. The server preflights
      // every item before anything moves, so one bad item fails the whole call.
      const res = await api.fmDelete(
        deleting.map((e) => ({
          path: e.path,
          confirm: e.kind === "dir" ? e.name : undefined,
        })),
      )
      if ("job" in res) setJobId(res.job.id)
      setDeleting(null)
      selection.clear()
      reload()
    } catch (err) {
      if (err instanceof ApiError && err.code === "confirmation_required") {
        setDeleteError(err.detail)
      } else {
        setDeleteError(fmErrorText(err, "The delete failed."))
      }
    } finally {
      setDeleteBusy(false)
    }
  }

  const archiveSelected = async () => {
    if (listing === null || selection.entries.length === 0) return
    try {
      const job = await api.fmArchive({
        dir: listing.path,
        names: selection.entries.map((e) => e.name),
        dest: `${listing.path === "/" ? "" : listing.path}/archive.tar.gz`,
        format: "tar.gz",
      })
      setJobId(job.id)
      selection.clear()
    } catch (err) {
      setError(fmErrorText(err, "The archive could not be started."))
    }
  }

  const fileInput = useRef<HTMLInputElement>(null)

  const submitExtract = async (dest: string) => {
    if (extracting === null) return
    setExtractError(null)
    try {
      const job = await api.fmExtract({ path: extracting.path, dest, overwrite: false })
      setJobId(job.id)
      setExtracting(null)
    } catch (err) {
      setExtractError(fmErrorText(err, "The extract could not be started."))
    }
  }

  return (
    // The page fills the space AppLayout's <main> gives it. Everything but the
    // listing is shrink-0, so only the listing scrolls.
    <div className="flex h-full min-h-[35rem] flex-col gap-3">
      <div
        className="border-border flex shrink-0 flex-wrap items-center gap-1 border-b"
        role="tablist"
        aria-label="Files views"
      >
        {(["files", "trash"] as const).map((t) => (
          <button
            key={t}
            type="button"
            role="tab"
            aria-selected={tab === t}
            onClick={() => setTab(t)}
            className={cn(
              "-mb-px flex items-center gap-1.5 border-b-2 px-3 py-1.5 text-sm font-medium capitalize",
              tab === t
                ? "border-primary text-foreground"
                : "text-muted-foreground hover:text-foreground border-transparent",
            )}
          >
            {t === "files" ? (
              <Folder className="size-3.5" aria-hidden="true" />
            ) : (
              <Trash2 className="size-3.5" aria-hidden="true" />
            )}
            {t === "files" ? "Files" : "Trash"}
          </button>
        ))}
      </div>

      {tab === "trash" ? (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <TrashPanel onChanged={reload} />
        </div>
      ) : (
        <>
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            <Button
              size="icon-sm"
              variant="outline"
              aria-label="Up one folder"
              disabled={path === "/"}
              onClick={() => setPath(path.slice(0, path.lastIndexOf("/")) || "/")}
            >
              <ArrowUp aria-hidden="true" />
            </Button>
            <div className="border-input bg-card flex h-7 min-w-0 flex-1 items-center overflow-x-auto rounded-md border px-2.5 pointer-coarse:h-10 [&_nav]:flex-nowrap [&_nav]:whitespace-nowrap">
              <FMBreadcrumbs path={path} onNavigate={setPath} />
            </div>
            <Button
              size="icon-sm"
              variant="outline"
              aria-label="Refresh"
              disabled={pending}
              onClick={reload}
            >
              <RefreshCw className={cn(pending && "animate-spin")} aria-hidden="true" />
            </Button>
            <div className="flex flex-wrap items-center gap-2">
              <label className="text-muted-foreground flex items-center gap-1.5 text-sm">
                <input
                  type="checkbox"
                  checked={hidden}
                  onChange={(e) => setHidden(e.target.checked)}
                  aria-label="Show hidden files"
                />
                Hidden
              </label>
              <Select
                aria-label="Sort by"
                value={sort}
                onChange={(e) => setSort(e.target.value as FMSort)}
                className="w-32"
              >
                <option value="name">Name</option>
                <option value="size">Size</option>
                <option value="mtime">Modified</option>
              </Select>
              <Button
                size="sm"
                variant="ghost"
                aria-pressed={desc}
                onClick={() => setDesc((d) => !d)}
              >
                {desc ? (
                  <ArrowUpAZ aria-hidden="true" />
                ) : (
                  <ArrowDownAZ aria-hidden="true" />
                )}
                {desc ? "Descending" : "Ascending"}
              </Button>
            </div>
          </div>

          <div className="flex shrink-0 flex-wrap items-center gap-2">
            <Button size="sm" variant="outline" onClick={() => setPrompt("mkdir")}>
              <FolderPlus aria-hidden="true" />
              New folder
            </Button>
            <Button size="sm" variant="outline" onClick={() => setPrompt("create")}>
              <FilePlus aria-hidden="true" />
              New file
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={listing === null}
              onClick={() => fileInput.current?.click()}
            >
              <Upload aria-hidden="true" />
              Upload here
            </Button>
            <span className="bg-border mx-1 hidden h-5 w-px sm:block" role="presentation" />
            <Button
              size="sm"
              variant="outline"
              disabled={selection.entries.length === 0}
              onClick={() => void archiveSelected()}
            >
              <Archive aria-hidden="true" />
              Archive selected
            </Button>
            {selection.entries.length === 1 && (
              <Button size="sm" variant="outline" onClick={() => setPrompt("rename")}>
                <Pencil aria-hidden="true" />
                Rename
              </Button>
            )}
            <Button
              size="sm"
              variant="destructive"
              disabled={selection.entries.length === 0}
              onClick={() => {
                setDeleteError(null)
                setDeleting(selection.entries)
              }}
            >
              <Trash2 aria-hidden="true" />
              Delete
            </Button>
            {selection.entries.length > 0 && (
              <span className="text-muted-foreground text-xs">
                {selection.entries.length} selected
              </span>
            )}
            <input
              ref={fileInput}
              type="file"
              multiple
              className="hidden"
              onChange={(e) => {
                const files = [...(e.target.files ?? [])]
                if (listing !== null && files.length > 0) uploadQueue.enqueue(listing.path, files)
                e.target.value = ""
              }}
            />
          </div>

          {error && (
            <p role="alert" className="text-destructive shrink-0 text-sm">
              {error}
            </p>
          )}

          {pending && listing === null ? (
            <p className="text-muted-foreground text-sm">Listing…</p>
          ) : listing !== null ? (
            // The one scroll container on the page: header row sticks, rows scroll.
            <div
              {...dragProps}
              className={cn(
                "bg-card relative min-h-0 flex-1 overflow-auto rounded-lg border transition-colors",
                pending && "opacity-60",
                dragging && "border-primary ring-primary/30 ring-2",
              )}
              aria-label={`Files in ${listing.path}. Drop files here to upload.`}
            >
              {dragging && (
                <div className="bg-primary/5 pointer-events-none absolute inset-0 z-20 flex items-center justify-center">
                  <p className="bg-card border-primary text-foreground rounded-md border px-4 py-2 text-sm font-medium shadow-sm">
                    Drop files to upload to {listing.path}
                  </p>
                </div>
              )}
              <table className="w-full text-sm">
                <thead className="bg-muted text-muted-foreground sticky top-0 z-10 text-left text-xs shadow-[0_1px_0_var(--border)]">
                  <tr>
                    <th scope="col" className="w-8 px-3 py-1.5">
                      <input
                        type="checkbox"
                        checked={selection.all}
                        onChange={() => selection.toggleAll()}
                        aria-label="Select all"
                      />
                    </th>
                    <th scope="col" className="px-3 py-1.5 font-medium">Name</th>
                    <th scope="col" className="px-3 py-1.5 font-medium">Size</th>
                    <th scope="col" className="px-3 py-1.5 font-medium">Mode</th>
                    <th scope="col" className="px-3 py-1.5 font-medium">Owner</th>
                    <th scope="col" className="px-3 py-1.5 font-medium">Modified</th>
                    <th scope="col" className="px-3 py-1.5 font-medium">
                      <span className="sr-only">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-border divide-y">
                  {listing.entries.length === 0 ? (
                    <tr>
                      <td colSpan={7} className="text-muted-foreground px-3 py-6 text-center">
                        This folder is empty.
                      </td>
                    </tr>
                  ) : (
                    listing.entries.map((entry) => (
                      <FileRow
                        key={entry.path}
                        entry={entry}
                        selected={selection.entries.some((e) => e.path === entry.path)}
                        onCheck={(shift) => selection.toggle(entry, shift)}
                        onOpen={() => open(entry)}
                        onMenu={() =>
                          setMenuFor((cur) => (cur === entry.path ? null : entry.path))
                        }
                        menuOpen={menuFor === entry.path}
                        onAction={(action) => {
                          setMenuFor(null)
                          if (action === "edit") setEditing(entry.path)
                          else if (action === "rename") setPrompt("rename")
                          else if (action === "extract") {
                            setExtractError(null)
                            setExtracting(entry)
                          } else if (action === "download")
                            window.open(api.fmDownloadUrl(entry.path), "_blank", "noopener")
                          else if (action === "download-folder")
                            window.open(
                              api.fmDownloadUrl(entry.path, { format: "tar.gz" }),
                              "_blank",
                              "noopener",
                            )
                        }}
                      />
                    ))
                  )}
                </tbody>
              </table>
            </div>
          ) : null}

          {listing !== null && (
            <div className="text-muted-foreground flex shrink-0 items-center justify-between gap-3 text-xs">
              <span>
                {listing.entries.filter((e) => e.kind === "dir").length} folders,{" "}
                {listing.entries.filter((e) => e.kind !== "dir").length} files
              </span>
              <span>
                {listing.entries.length < listing.total
                  ? `Showing ${listing.entries.length} of ${listing.total}`
                  : `Total ${listing.total}`}
              </span>
            </div>
          )}

          <UploadQueuePanel onQuiet={reload} className="max-h-40 shrink-0 overflow-y-auto" />
        </>
      )}

      {prompt !== null && (
        <NameDialog
          kind={prompt}
          path={prompt === "rename" ? (selection.entries[0]?.path ?? "") : path}
          initialName={prompt === "rename" ? selection.entries[0]?.name : undefined}
          onDone={reload}
          onOpenChange={(open) => !open && setPrompt(null)}
        />
      )}

      {editing !== null && (
        <EditorDialog path={editing} onClose={() => setEditing(null)} onSaved={reload} />
      )}

      {deleting !== null && (
        <ConfirmDialog
          title={
            deleting.length === 1
              ? `Delete ${deleting[0].name}?`
              : `Delete ${deleting.length} items?`
          }
          description="Items move to the trash and stay there for 30 days. A folder asks for its name typed."
          confirmText={
            deleting.length === 1 && deleting[0].kind === "dir" ? deleting[0].name : undefined
          }
          actionLabel="Move to trash"
          busy={deleteBusy}
          error={deleteError}
          onConfirm={() => void deleteSelected()}
          onOpenChange={(open) => !open && setDeleting(null)}
        />
      )}

      {extracting !== null && (
        <ExtractDialog
          archive={extracting.path}
          parent={extracting.path.slice(0, extracting.path.lastIndexOf("/")) || "/"}
          defaultName={extracting.name.replace(/\.(zip|tar|tar\.gz|tgz)$/i, "")}
          busy={false}
          error={extractError}
          onConfirm={(dest) => void submitExtract(dest)}
          onOpenChange={(open) => {
            if (!open) {
              setExtracting(null)
              setExtractError(null)
            }
          }}
        />
      )}

      <JobsDrawer jobId={jobId} onClose={() => setJobId(null)} />
    </div>
  )
}

/** The row actions a context menu offers, per kind. */
export type RowAction = "edit" | "rename" | "extract" | "download" | "download-folder"

/** A file name that names an archive the server can extract. */
export function isArchiveName(name: string): boolean {
  return /\.(zip|tar|tar\.gz|tgz)$/i.test(name)
}

function FileRow({
  entry,
  selected,
  onCheck,
  onOpen,
  onMenu,
  menuOpen,
  onAction,
}: {
  entry: FMEntry
  selected: boolean
  onCheck: (shift: boolean) => void
  onOpen: () => void
  onMenu: () => void
  menuOpen: boolean
  onAction: (action: RowAction) => void
}) {
  const dirLike = entry.kind === "dir" || (entry.kind === "symlink" && entry.target_kind === "dir")
  const actions: { key: RowAction; label: string }[] = [
    ...(entry.kind === "file" ? [{ key: "edit" as const, label: "Edit" }] : []),
    { key: "rename" as const, label: "Rename" },
    ...(entry.kind === "file" && isArchiveName(entry.name)
      ? [{ key: "extract" as const, label: "Extract…" }]
      : []),
    ...(dirLike
      ? [{ key: "download-folder" as const, label: "Download as .tar.gz" }]
      : [{ key: "download" as const, label: "Download" }]),
  ]

  const menuButton = useRef<HTMLButtonElement>(null)

  // Close the menu when anything scrolls, since a fixed menu would stay put
  // while its row moves away.
  useEffect(() => {
    if (!menuOpen) return
    const close = () => onMenu()
    window.addEventListener("scroll", close, true)
    window.addEventListener("resize", close)
    return () => {
      window.removeEventListener("scroll", close, true)
      window.removeEventListener("resize", close)
    }
  }, [menuOpen, onMenu])

  let menuStyle: CSSProperties | undefined
  if (menuOpen && menuButton.current) {
    const r = menuButton.current.getBoundingClientRect()
    const menuHeight = actions.length * 32 + 8
    const flip = r.bottom + menuHeight > window.innerHeight
    menuStyle = {
      right: Math.max(8, window.innerWidth - r.right),
      ...(flip ? { bottom: window.innerHeight - r.top + 4 } : { top: r.bottom + 4 }),
    }
  }

  return (
    <tr
      className={cn("hover:bg-accent/40 transition-colors", selected && "bg-accent/50")}
      data-path={entry.path}
    >
      <td className="px-3 py-1.5">
        <input
          type="checkbox"
          checked={selected}
          onChange={(e) => onCheck(e.nativeEvent instanceof MouseEvent && e.nativeEvent.shiftKey)}
          aria-label={`Select ${entry.name}`}
        />
      </td>
      <td className="px-3 py-1.5">
        <button
          type="button"
          className="flex items-center gap-2 text-left font-medium underline-offset-4 hover:underline"
          onClick={onOpen}
        >
          {entry.kind === "symlink" ? (
            <Link2 className="text-muted-foreground size-4 shrink-0" aria-hidden="true" />
          ) : dirLike ? (
            <Folder className="size-4 shrink-0 fill-amber-400/30 text-amber-500" aria-hidden="true" />
          ) : (
            <FileIcon className="text-muted-foreground size-4 shrink-0" aria-hidden="true" />
          )}
          <span>{entry.name}</span>
          {entry.kind === "symlink" && (
            <span className="text-muted-foreground text-xs font-normal">→ {entry.target}</span>
          )}
        </button>
      </td>
      <td className="text-muted-foreground px-3 py-1.5 whitespace-nowrap">
        {formatSize(entry.size, dirLike)}
      </td>
      <td className="text-muted-foreground px-3 py-1.5 font-mono text-xs">
        {formatMode(entry.mode)}
      </td>
      <td className="text-muted-foreground px-3 py-1.5 whitespace-nowrap">
        {entry.owner || entry.uid}
      </td>
      <td className="text-muted-foreground px-3 py-1.5 whitespace-nowrap">
        {formatMtime(entry.mtime)}
      </td>
      <td className="px-3 py-1.5 text-right">
        <div className="flex justify-end">
          <Button
            ref={menuButton}
            size="icon-sm"
            variant="ghost"
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            aria-label={`Actions for ${entry.name}`}
            onClick={onMenu}
          >
            <MoreHorizontal aria-hidden="true" />
          </Button>
          {menuOpen && (
            // Fixed, not absolute: the listing scrolls and clips, and a menu
            // under the last row must not be cut off by it.
            <div
              role="menu"
              aria-label={`Actions for ${entry.name}`}
              style={menuStyle}
              className="bg-popover text-popover-foreground fixed z-50 w-44 rounded-md border p-1 shadow-lg"
            >
              {actions.map((a) => (
                <button
                  key={a.key}
                  type="button"
                  role="menuitem"
                  className="hover:bg-accent w-full rounded-sm px-2 py-1.5 text-left text-sm"
                  onClick={() => onAction(a.key)}
                >
                  {a.label}
                </button>
              ))}
            </div>
          )}
        </div>
      </td>
    </tr>
  )
}
