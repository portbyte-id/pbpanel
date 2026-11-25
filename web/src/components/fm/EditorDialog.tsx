import { canAutoFocus, tablistKeyDown } from "@/lib/utils"
import { gotoLine, openSearchPanel } from "@codemirror/search"
import type { EditorView } from "@codemirror/view"
import {
  ArrowUp,
  FilePlus,
  FileText,
  Folder,
  Hash,
  Keyboard,
  Moon,
  PanelLeft,
  RefreshCw,
  Replace,
  Save,
  SaveAll,
  Search,
  Settings,
  Sun,
  Type,
  X,
} from "lucide-react"
import { useEffect, useRef, useState } from "react"

import { ConfirmDialog } from "@/components/ConfirmDialog"
import { languageLabel } from "@/components/code/codemirror"
import { fmErrorText } from "@/components/fm/FMShared"
import { type EditorPalette, type EditorStatus, statusOf, TextEditor } from "@/components/fm/TextEditor"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { SkeletonRows } from "@/components/ui/skeleton"
import { api, ApiError, type FMBase, type FMEntry, type FMFile } from "@/lib/api"

/** A file as the server opened it, with the language it names. */
type Opened = FMFile & { language: string }

/** One open file. `draft` is the editor's text with LF line endings; null while it matches the file. */
interface Tab {
  path: string
  name: string
  file: Opened | null
  loadError: string | null
  draft: string | null
  saveError: string | null
}

interface Prefs {
  fontSize: number
  tabSize: number
  wrap: boolean
  palette: EditorPalette
  showFiles: boolean
}

interface Confirm {
  title: string
  body: string
  label: string
  run: () => void
}

const PREFS_KEY = "panel.editor"
const FONT_SIZES = [12, 13, 14, 16, 18]
const TAB_SIZES = [2, 4, 8]
const PALETTES: EditorPalette[] = ["auto", "light", "dark"]
const defaultPrefs: Prefs = { fontSize: 13, tabSize: 4, wrap: false, palette: "auto", showFiles: true }

const SHORTCUTS: [string, string][] = [
  ["Ctrl+S", "Save the open file"],
  ["Ctrl+F", "Find, with the replace field under it"],
  ["F3 / Ctrl+G", "Find next"],
  ["Ctrl+Alt+G", "Jump to line"],
  ["Ctrl+D", "Select the next occurrence"],
  ["Ctrl+Z / Ctrl+Y", "Undo / redo"],
  ["Tab", "Indent the selection"],
  ["Esc", "Close the find panel"],
]

/** Editor preferences live in this browser only, so a bad or missing value falls back to the default. */
function readPrefs(): Prefs {
  try {
    const raw = window.localStorage.getItem(PREFS_KEY)
    if (raw) {
      const v = JSON.parse(raw) as Partial<Record<keyof Prefs, unknown>>
      return {
        fontSize: FONT_SIZES.includes(v.fontSize as number) ? (v.fontSize as number) : defaultPrefs.fontSize,
        tabSize: TAB_SIZES.includes(v.tabSize as number) ? (v.tabSize as number) : defaultPrefs.tabSize,
        wrap: typeof v.wrap === "boolean" ? v.wrap : defaultPrefs.wrap,
        palette: PALETTES.includes(v.palette as EditorPalette)
          ? (v.palette as EditorPalette)
          : defaultPrefs.palette,
        showFiles: typeof v.showFiles === "boolean" ? v.showFiles : defaultPrefs.showFiles,
      }
    }
  } catch {
    // Unreadable or blocked storage: the defaults are fine.
  }
  return defaultPrefs
}

function writePrefs(prefs: Prefs) {
  try {
    window.localStorage.setItem(PREFS_KEY, JSON.stringify(prefs))
  } catch {
    // Not kept; the editor still works for this visit.
  }
}

/** The editor works in LF. A CRLF file is converted on the way in and back on the way to disk. */
const toLF = (text: string) => text.replace(/\r\n/g, "\n")
const toDisk = (text: string, crlf: boolean) => (crlf ? text.replace(/\n/g, "\r\n") : text)
const isCRLF = (text: string) => text.includes("\r\n")

function baseName(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1)
}

function parentOf(path: string): string {
  const i = path.lastIndexOf("/")
  return i <= 0 ? "/" : path.slice(0, i)
}

function joinPath(dir: string, name: string): string {
  return dir.endsWith("/") ? `${dir}${name}` : `${dir}/${name}`
}

function newTab(path: string): Tab {
  return { path, name: baseName(path), file: null, loadError: null, draft: null, saveError: null }
}

/** What the draft is compared with: the file as it was last read or saved. */
function baselineOf(tab: Tab): string {
  return toLF(tab.file?.content ?? "")
}

function isDirty(tab: Tab): boolean {
  return tab.draft !== null && tab.draft !== baselineOf(tab)
}

function shownText(tab: Tab): string {
  return tab.draft ?? toLF(tab.file?.content ?? "")
}

function dirsFirst(a: FMEntry, b: FMEntry): number {
  return Number(b.kind === "dir") - Number(a.kind === "dir")
}

/**
 * The text-file editor as a workspace: a directory list, a tab per open file,
 * CodeMirror with search and go-to-line, and a status line.
 *
 * Saving writes the draft to disk. A save carries the `base` the file was opened
 * with; if the file changed on disk since, the server answers 409 `edit_conflict`
 * with the disk copy, and the save is retried once against that copy, so the
 * draft replaces it without a prompt.
 */
export function EditorDialog({
  path,
  onClose,
  onSaved,
}: {
  path: string
  onClose: () => void
  onSaved: () => void
}) {
  const [tabs, setTabs] = useState<Tab[]>(() => [newTab(path)])
  const [active, setActive] = useState(path)
  const [prefs, setPrefs] = useState<Prefs>(readPrefs)
  const [status, setStatus] = useState<EditorStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)
  const [confirm, setConfirm] = useState<Confirm | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [shortcutsOpen, setShortcutsOpen] = useState(false)

  const [dir, setDir] = useState(parentOf(path))
  const [entries, setEntries] = useState<FMEntry[] | null>(null)
  const [listError, setListError] = useState<string | null>(null)
  const [listKey, setListKey] = useState(0)
  const [filter, setFilter] = useState("")
  const [newName, setNewName] = useState<string | null>(null)
  const [treeError, setTreeError] = useState<string | null>(null)

  // Live views by path, for the toolbar's search and go-to-line.
  const views = useRef(new Map<string, EditorView>())
  // Paths that already have a tab, so opening one again only switches to it.
  const known = useRef(new Set<string>([path]))
  // The latest active path, for listeners that outlive a render.
  const activeRef = useRef(active)
  activeRef.current = active

  const current = tabs.find((t) => t.path === active) ?? null
  const dirty = tabs.filter(isDirty)
  const crlf = current?.file ? isCRLF(current.file.content) : false

  // A reload or tab close would drop unsaved edits: ask the browser to confirm.
  const hasDirty = dirty.length > 0
  useEffect(() => {
    if (!hasDirty) return
    const warn = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener("beforeunload", warn)
    return () => window.removeEventListener("beforeunload", warn)
  }, [hasDirty])

  useEffect(() => {
    writePrefs(prefs)
  }, [prefs])

  // The status line follows whichever file is active, not the last one edited.
  useEffect(() => {
    const v = views.current.get(active)
    setStatus(v ? statusOf(v.state) : null)
  }, [active])

  useEffect(() => {
    let live = true
    setEntries(null)
    api
      .fmList({ path: dir, sort: "name", hidden: true })
      .then((listing) => {
        if (!live) return
        setEntries([...listing.entries].sort(dirsFirst))
        setListError(null)
      })
      .catch((err: unknown) => {
        if (!live) return
        setEntries([])
        setListError(fmErrorText(err, "The folder could not be listed."))
      })
    return () => {
      live = false
    }
  }, [dir, listKey])

  // The file the dialog opened with loads once; the rest load when opened.
  useEffect(() => {
    void load(path)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const patch = (p: string, change: Partial<Tab>) =>
    setTabs((ts) => ts.map((t) => (t.path === p ? { ...t, ...change } : t)))

  const load = (p: string) =>
    api
      .fmOpenFile(p)
      .then((f) => patch(p, { file: f, loadError: null, draft: null, saveError: null }))
      .catch((err: unknown) =>
        patch(p, { loadError: fmErrorText(err, "The file could not be opened.") }),
      )

  const openFile = (p: string) => {
    if (!known.current.has(p)) {
      known.current.add(p)
      setTabs((ts) => [...ts, newTab(p)])
      void load(p)
    }
    setActive(p)
  }

  const closeTab = (p: string) => {
    const tab = tabs.find((t) => t.path === p)
    if (!tab) return
    const remove = () => {
      const i = tabs.findIndex((t) => t.path === p)
      const rest = tabs.filter((t) => t.path !== p)
      known.current.delete(p)
      setTabs(rest)
      if (activeRef.current === p) setActive(rest[i]?.path ?? rest[i - 1]?.path ?? "")
    }
    if (isDirty(tab)) {
      setConfirm({
        title: `Close ${tab.name} without saving?`,
        body: "Its unsaved changes are lost.",
        label: "Discard changes",
        run: remove,
      })
    } else {
      remove()
    }
  }

  const reloadTab = (p: string) => {
    const tab = tabs.find((t) => t.path === p)
    if (!tab) return
    const run = () => {
      setNotice(null)
      void load(p)
    }
    if (isDirty(tab)) {
      setConfirm({
        title: `Reload ${tab.name} from disk?`,
        body: "Your unsaved changes are replaced by the file on disk.",
        label: "Reload and discard",
        run,
      })
    } else {
      run()
    }
  }

  /**
   * Saves one tab to disk. Returns whether it reached disk. A file changed since
   * it was opened is not a question: the disk copy the server reports becomes the
   * base and the draft is written over it, once.
   */
  const persist = async (tab: Tab): Promise<boolean> => {
    if (!tab.file || tab.draft === null) return false
    const content = toDisk(tab.draft, isCRLF(tab.file.content))
    let from: FMFile = tab.file
    for (let attempt = 0; attempt < 2; attempt++) {
      const base: FMBase = { size: from.size, mtime: from.mtime, hash: from.hash }
      try {
        const saved = await api.fmSaveFile(tab.path, content, base)
        patch(tab.path, {
          file: { ...saved, language: tab.file.language },
          draft: null,
          saveError: null,
        })
        onSaved()
        return true
      } catch (err) {
        if (err instanceof ApiError && err.code === "edit_conflict" && err.conflictCurrent) {
          from = err.conflictCurrent
          continue
        }
        patch(tab.path, { saveError: fmErrorText(err, "The file could not be saved.") })
        return false
      }
    }
    patch(tab.path, { saveError: "The file changed again while it was being saved. Save once more." })
    return false
  }

  const saveTab = async (p: string) => {
    const tab = tabs.find((t) => t.path === p)
    if (!tab) return
    setBusy(true)
    setNotice(null)
    const ok = await persist(tab)
    setBusy(false)
    if (ok) setNotice(`Saved ${tab.name}.`)
  }

  const saveAll = async () => {
    setBusy(true)
    setNotice(null)
    let saved = 0
    for (const tab of dirty) {
      if (await persist(tab)) saved++
    }
    setBusy(false)
    setNotice(saved === 0 ? "Nothing was saved." : `Saved ${saved} file${saved === 1 ? "" : "s"}.`)
  }

  const requestClose = () => {
    if (dirty.length === 0) {
      onClose()
      return
    }
    setConfirm({
      title: "Close the editor?",
      body: `${dirty.length} ${dirty.length === 1 ? "file has" : "files have"} unsaved changes. Closing discards them.`,
      label: "Close and discard",
      run: onClose,
    })
  }

  const viewOf = () => views.current.get(activeRef.current)

  const showSearch = (replace: boolean) => {
    const v = viewOf()
    if (!v) return
    openSearchPanel(v)
    if (replace) {
      window.setTimeout(() => {
        v.dom.querySelector<HTMLInputElement>('.cm-search input[name="replace"]')?.focus()
      }, 0)
    }
  }

  const createFile = async () => {
    const name = (newName ?? "").trim()
    if (name === "" || name.includes("/")) {
      setTreeError("Type a file name without a slash.")
      return
    }
    try {
      const entry = await api.fmCreateFile(joinPath(dir, name))
      setNewName(null)
      setTreeError(null)
      setListKey((k) => k + 1)
      openFile(entry.path)
    } catch (err) {
      setTreeError(fmErrorText(err, "The file could not be created."))
    }
  }

  const update = (change: Partial<Prefs>) => setPrefs((p) => ({ ...p, ...change }))
  const nextPalette = PALETTES[(PALETTES.indexOf(prefs.palette) + 1) % PALETTES.length]
  const PaletteIcon = prefs.palette === "dark" ? Moon : Sun
  const visibleEntries = entries?.filter((e) =>
    e.name.toLowerCase().includes(filter.trim().toLowerCase()),
  )

  return (
    <Dialog open onOpenChange={(open) => !open && requestClose()}>
      <DialogContent className="flex h-[min(92svh,56rem)] w-[min(96vw,84rem)] max-w-[min(96vw,84rem)] flex-col gap-0 overflow-hidden p-0 sm:max-w-[min(96vw,84rem)]">
        <DialogHeader className="border-border gap-0.5 border-b px-4 py-3">
          <DialogTitle>Online Text Editor</DialogTitle>
          <DialogDescription>
            Save writes the draft to disk, replacing the file's contents.
          </DialogDescription>
        </DialogHeader>

        <div
          role="toolbar"
          aria-label="Editor"
          className="border-border bg-muted/40 flex flex-wrap items-center gap-1 border-b px-2 py-1.5"
        >
          <Button
            size="sm"
            disabled={!current?.file || !isDirty(current) || busy}
            onClick={() => current && void saveTab(current.path)}
          >
            <Save aria-hidden />
            Save
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={dirty.length === 0 || busy}
            onClick={() => void saveAll()}
          >
            <SaveAll aria-hidden />
            Save All
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={!current}
            onClick={() => current && reloadTab(current.path)}
          >
            <RefreshCw aria-hidden />
            Refresh
          </Button>
          <Button size="sm" variant="ghost" disabled={!current?.file} onClick={() => showSearch(false)}>
            <Search aria-hidden />
            Search
          </Button>
          <Button size="sm" variant="ghost" disabled={!current?.file} onClick={() => showSearch(true)}>
            <Replace aria-hidden />
            Replace
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={!current?.file}
            onClick={() => {
              const v = viewOf()
              if (v) gotoLine(v)
            }}
          >
            <Hash aria-hidden />
            Jump Line
          </Button>
          <label className="text-muted-foreground flex items-center gap-1 text-xs">
            <Type className="size-3.5" aria-hidden />
            <span className="sr-only sm:not-sr-only">Font Size</span>
            <select
              aria-label="Font size"
              value={prefs.fontSize}
              onChange={(e) => update({ fontSize: Number(e.target.value) })}
              className="border-input bg-card text-foreground h-7 rounded-md border px-1.5 text-xs"
            >
              {FONT_SIZES.map((size) => (
                <option key={size} value={size}>
                  {size}px
                </option>
              ))}
            </select>
          </label>
          <Button
            size="sm"
            variant="ghost"
            aria-label={`Theme: ${prefs.palette}. Switch to ${nextPalette}`}
            onClick={() => update({ palette: nextPalette })}
          >
            <PaletteIcon aria-hidden />
            <span className="capitalize">Theme: {prefs.palette}</span>
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setSettingsOpen(true)}>
            <Settings aria-hidden />
            Settings
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setShortcutsOpen(true)}>
            <Keyboard aria-hidden />
            Shortcuts
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="ml-auto"
            aria-pressed={prefs.showFiles}
            onClick={() => update({ showFiles: !prefs.showFiles })}
          >
            <PanelLeft aria-hidden />
            {prefs.showFiles ? "Hide files" : "Show files"}
          </Button>
        </div>

        <div className="flex min-h-0 flex-1 flex-col md:flex-row">
          {prefs.showFiles && (
            <aside
              aria-label="Directory"
              className="border-border flex max-h-[42%] min-h-0 flex-col border-b md:max-h-none md:w-64 md:shrink-0 md:border-r md:border-b-0"
            >
              <div className="flex items-center gap-1 px-2 pt-2">
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label="Up one folder"
                  disabled={dir === "/"}
                  onClick={() => {
                    setDir(parentOf(dir))
                    setFilter("")
                  }}
                >
                  <ArrowUp aria-hidden />
                </Button>
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label="Refresh folder"
                  onClick={() => setListKey((k) => k + 1)}
                >
                  <RefreshCw aria-hidden />
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label="New file"
                  onClick={() => {
                    setNewName("")
                    setTreeError(null)
                  }}
                >
                  <FilePlus aria-hidden />
                  New
                </Button>
              </div>
              <p className="text-muted-foreground truncate px-2 pt-1 text-xs" title={dir}>
                Directory: <code translate="no" className="text-foreground font-mono">{dir}</code>
              </p>
              <div className="px-2 py-2">
                <Input
                  aria-label="Filter this folder"
                  placeholder="Filter this folder…"
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                  className="h-8 text-xs"
                />
              </div>

              {newName !== null && (
                <form
                  className="flex gap-1 px-2 pb-2"
                  onSubmit={(e) => {
                    e.preventDefault()
                    void createFile()
                  }}
                >
                  <Input
                    aria-label="New file name"
                    autoFocus={canAutoFocus()}
                    value={newName}
                    onChange={(e) => setNewName(e.target.value)}
                    className="h-8 text-xs"
                  />
                  <Button type="submit" size="sm">
                    Create
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setNewName(null)
                      setTreeError(null)
                    }}
                  >
                    Cancel
                  </Button>
                </form>
              )}
              {(treeError || listError) && (
                <p role="alert" className="text-destructive px-2 pb-2 text-xs">
                  {treeError ?? listError}
                </p>
              )}

              <ul aria-label="Files in this folder" className="min-h-0 flex-1 overflow-y-auto px-1 pb-2">
                {visibleEntries === undefined && (
                  <li className="px-2 py-1">
                    <SkeletonRows label="Loading the folder…" />
                  </li>
                )}
                {visibleEntries?.length === 0 && (
                  <li className="text-muted-foreground px-2 py-1.5 text-xs">
                    {filter.trim() ? "Nothing matches." : "This folder is empty."}
                  </li>
                )}
                {visibleEntries?.map((e) => {
                  const isDir = e.kind === "dir"
                  const isActive = !isDir && e.path === active
                  return (
                    <li key={e.path}>
                      <button
                        type="button"
                        title={e.path}
                        aria-current={isActive ? "true" : undefined}
                        onClick={() => {
                          if (isDir) {
                            setDir(e.path)
                            setFilter("")
                          } else {
                            openFile(e.path)
                          }
                        }}
                        className={`flex min-h-8 w-full items-center gap-2 rounded px-2 text-left text-xs ${
                          isActive ? "bg-accent text-accent-foreground" : "hover:bg-accent/60"
                        }`}
                      >
                        {isDir ? (
                          <Folder className="text-primary size-3.5 shrink-0" aria-hidden />
                        ) : (
                          <FileText className="text-muted-foreground size-3.5 shrink-0" aria-hidden />
                        )}
                        <span className="truncate">{e.name}</span>
                      </button>
                    </li>
                  )
                })}
              </ul>
            </aside>
          )}

          <section className="flex min-h-0 min-w-0 flex-1 flex-col">
            <div
              role="tablist"
              onKeyDown={tablistKeyDown}
              aria-label="Open files"
              className="border-border bg-muted/30 flex min-w-0 overflow-x-auto border-b"
            >
              {tabs.map((t) => {
                const isActive = t.path === active
                return (
                  <div
                    key={t.path}
                    className={`border-border flex shrink-0 items-center border-r ${
                      isActive ? "bg-background" : ""
                    }`}
                  >
                    <button
                      type="button"
                      role="tab"
                      aria-selected={isActive}
                      tabIndex={isActive ? 0 : -1}
                      title={t.path}
                      onClick={() => setActive(t.path)}
                      className={`flex items-center gap-1.5 border-b-2 px-3 py-1.5 text-sm ${
                        isActive
                          ? "border-primary text-foreground font-medium"
                          : "text-muted-foreground hover:text-foreground border-transparent"
                      }`}
                    >
                      <span className="max-w-44 truncate font-mono text-xs">{t.name}</span>
                      {isDirty(t) && (
                        <span aria-label="unsaved changes" className="bg-primary size-1.5 rounded-full" />
                      )}
                    </button>
                    <button
                      type="button"
                      aria-label={`Close ${t.name}`}
                      onClick={() => closeTab(t.path)}
                      className="text-muted-foreground hover:text-foreground px-1.5 py-1.5"
                    >
                      <X className="size-3.5" aria-hidden />
                    </button>
                  </div>
                )
              })}
            </div>

            {current?.saveError && (
              <p role="alert" className="text-destructive border-border border-b px-3 py-2 text-sm">
                {current.saveError}
              </p>
            )}
            {!current?.saveError && notice && (
              <p role="status" className="text-muted-foreground border-border border-b px-3 py-1.5 text-xs">
                {notice}
              </p>
            )}

            <div className="flex min-h-0 flex-1 flex-col gap-2 p-2">
              {tabs.map((t) =>
                t.file ? (
                  <TextEditor
                    key={t.path}
                    label={`Contents of ${t.path}`}
                    value={shownText(t)}
                    language={t.file.language}
                    fontSize={prefs.fontSize}
                    tabSize={prefs.tabSize}
                    wrap={prefs.wrap}
                    palette={prefs.palette}
                    hidden={t.path !== active}
                    onChange={(text) => patch(t.path, { draft: text })}
                    onStatus={(s) => {
                      if (activeRef.current === t.path) setStatus(s)
                    }}
                    onSave={() => void saveTab(activeRef.current)}
                    onView={(v) => {
                      if (v) views.current.set(t.path, v)
                      else views.current.delete(t.path)
                    }}
                  />
                ) : null,
              )}
              {current && !current.file && !current.loadError && (
                <SkeletonRows label="Loading the file…" />
              )}
              {current?.loadError && (
                <p role="alert" className="text-destructive p-2 text-sm">
                  {current.loadError}
                </p>
              )}
              {!current && (
                <p className="text-muted-foreground p-4 text-sm">
                  No file is open. Choose one from the directory.
                </p>
              )}
            </div>
          </section>
        </div>

        <footer className="border-border text-muted-foreground flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-t px-3 py-1.5 text-xs">
          <span className="min-w-0 truncate font-mono" title={active}>
            File: {active || "none"}
          </span>
          {current?.file && (
            <span className="flex flex-wrap gap-x-4 gap-y-0.5">
              <span>{crlf ? "CRLF" : "LF"}</span>
              <span>
                Ln {status?.line ?? 1}, Col {status?.col ?? 1}
              </span>
              <span>History: {status?.history ?? 0}</span>
              <span>Tab: {prefs.tabSize}</span>
              <span>Encoding: UTF-8</span>
              <span>Language: {languageLabel(current.file.language)}</span>
            </span>
          )}
        </footer>

        {confirm && (
          <ConfirmDialog
            open
            onOpenChange={(open) => !open && setConfirm(null)}
            title={confirm.title}
            confirmLabel={confirm.label}
            pendingLabel={confirm.label}
            pending={false}
            destructive
            onConfirm={() => {
              const run = confirm.run
              setConfirm(null)
              run()
            }}
          >
            <p>{confirm.body}</p>
          </ConfirmDialog>
        )}
      </DialogContent>

      <Dialog open={settingsOpen} onOpenChange={setSettingsOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Editor settings</DialogTitle>
            <DialogDescription>These are kept in this browser.</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3 text-sm">
            <label className="flex items-center justify-between gap-3">
              Tab size
              <select
                value={prefs.tabSize}
                onChange={(e) => update({ tabSize: Number(e.target.value) })}
                className="border-input bg-card h-8 rounded-md border px-2"
              >
                {TAB_SIZES.map((size) => (
                  <option key={size} value={size}>
                    {size}
                  </option>
                ))}
              </select>
            </label>
            <label className="flex items-center justify-between gap-3">
              Word wrap
              <input
                type="checkbox"
                checked={prefs.wrap}
                onChange={(e) => update({ wrap: e.target.checked })}
              />
            </label>
            <label className="flex items-center justify-between gap-3">
              Show the directory list
              <input
                type="checkbox"
                checked={prefs.showFiles}
                onChange={(e) => update({ showFiles: e.target.checked })}
              />
            </label>
          </div>
        </DialogContent>
      </Dialog>

      <Dialog open={shortcutsOpen} onOpenChange={setShortcutsOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Shortcuts</DialogTitle>
            <DialogDescription>Inside the editor.</DialogDescription>
          </DialogHeader>
          <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
            {SHORTCUTS.map(([keys, what]) => (
              <div key={keys} className="contents">
                <dt>
                  <kbd className="bg-muted rounded px-1.5 py-0.5 font-mono text-xs">{keys}</kbd>
                </dt>
                <dd className="text-muted-foreground">{what}</dd>
              </div>
            ))}
          </dl>
        </DialogContent>
      </Dialog>
    </Dialog>
  )
}
