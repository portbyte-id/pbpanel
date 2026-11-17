import { useCallback, useEffect, useRef, useState } from "react"

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
import { Badge } from "@/components/ui/badge"
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
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2" role="tablist" aria-label="Files views">
        <Button
          role="tab"
          aria-selected={tab === "files"}
          variant={tab === "files" ? "default" : "ghost"}
          size="sm"
          onClick={() => setTab("files")}
        >
          Files
        </Button>
        <Button
          role="tab"
          aria-selected={tab === "trash"}
          variant={tab === "trash" ? "default" : "ghost"}
          size="sm"
          onClick={() => setTab("trash")}
        >
          Trash
        </Button>
      </div>

      {tab === "trash" ? (
        <TrashPanel onChanged={reload} />
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <FMBreadcrumbs path={path} onNavigate={setPath} />
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
                {desc ? "Descending" : "Ascending"}
              </Button>
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" variant="outline" onClick={() => setPrompt("mkdir")}>
              New folder
            </Button>
            <Button size="sm" variant="outline" onClick={() => setPrompt("create")}>
              New file
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={listing === null}
              onClick={() => fileInput.current?.click()}
            >
              Upload here
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={selection.entries.length === 0}
              onClick={() => void archiveSelected()}
            >
              Archive selected
            </Button>
            {selection.entries.length === 1 && (
              <Button size="sm" variant="outline" onClick={() => setPrompt("rename")}>
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
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          )}

          {pending && listing === null ? (
            <p className="text-muted-foreground text-sm">Listing…</p>
          ) : listing !== null ? (
            <div className="overflow-x-auto rounded-lg border">
              <table className="w-full text-sm">
                <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
                  <tr>
                    <th className="w-8 px-3 py-1.5">
                      <input
                        type="checkbox"
                        checked={selection.all}
                        onChange={() => selection.toggleAll()}
                        aria-label="Select all"
                      />
                    </th>
                    <th className="px-3 py-1.5 font-medium">Name</th>
                    <th className="px-3 py-1.5 font-medium">Size</th>
                    <th className="px-3 py-1.5 font-medium">Mode</th>
                    <th className="px-3 py-1.5 font-medium">Owner</th>
                    <th className="px-3 py-1.5 font-medium">Modified</th>
                    <th className="px-3 py-1.5 font-medium">
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

          <UploadQueuePanel onQuiet={reload} />
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

  return (
    <tr className={cn(selected && "bg-accent/50")} data-path={entry.path}>
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
          className="text-left font-medium underline-offset-4 hover:underline"
          onClick={onOpen}
        >
          {entry.name}
          {entry.kind === "symlink" && (
            <span className="text-muted-foreground ml-1.5 text-xs">→ {entry.target}</span>
          )}
        </button>
        {dirLike && entry.kind !== "symlink" && (
          <Badge variant="secondary" className="ml-2">
            folder
          </Badge>
        )}
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
        <div className="relative flex justify-end">
          <Button
            size="sm"
            variant="ghost"
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            aria-label={`Actions for ${entry.name}`}
            onClick={onMenu}
          >
            ⋯
          </Button>
          {menuOpen && (
            <div
              role="menu"
              aria-label={`Actions for ${entry.name}`}
              className="bg-background absolute right-0 z-10 mt-8 w-44 rounded-md border p-1 shadow-lg"
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
