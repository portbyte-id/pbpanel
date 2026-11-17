import { SkeletonRows } from "@/components/ui/skeleton"
import { useEffect, useState } from "react"

import { ConfigEditor } from "@/components/ConfigEditor"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { api, ApiError, type FMBase, type FMFile } from "@/lib/api"
import { fmErrorText } from "@/components/fm/FMShared"

/**
 * The text-file editor as a dialog, wired to the fm open/save endpoints.
 *
 * A save carries the `base` the file was opened with; the server answers 409
 * `edit_conflict` when the file changed on disk since then, with the file as it
 * is now. The dialog keeps the operator's draft, shows the disk version beside
 * it, and lets them overwrite the disk copy or reload it — never silently
 * picks one.
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
  const [file, setFile] = useState<FMFile | null>(null)
  const [draft, setDraft] = useState<string | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [conflict, setConflict] = useState<FMFile | null>(null)
  const [busy, setBusy] = useState(false)
  const [savedAt, setSavedAt] = useState<FMFile | null>(null)

  useEffect(() => {
    let alive = true
    setFile(null)
    setDraft(null)
    setLoadError(null)
    setSaveError(null)
    setConflict(null)
    setSavedAt(null)
    api
      .fmOpenFile(path)
      .then((f) => {
        if (alive) setFile(f)
      })
      .catch((err) => {
        if (alive) setLoadError(fmErrorText(err, "The file could not be opened."))
      })
    return () => {
      alive = false
    }
  }, [path])

  const base: FMBase | null =
    file === null
      ? null
      : { size: file.size, mtime: file.mtime, hash: file.hash }

  const save = async (overwrite: FMFile | null) => {
    if (draft === null) return
    setBusy(true)
    setSaveError(null)
    try {
      const useBase = overwrite
        ? { size: overwrite.size, mtime: overwrite.mtime, hash: overwrite.hash }
        : base
      const saved = await api.fmSaveFile(path, draft, useBase as FMBase)
      setFile(saved)
      setDraft(null)
      setConflict(null)
      setSavedAt(saved)
      onSaved()
    } catch (err) {
      if (err instanceof ApiError && err.code === "edit_conflict") {
        setConflict(err.conflictCurrent)
      } else if (err instanceof ApiError && err.code === "use_config_editor") {
        setSaveError(
          `${err.detail} This file is managed by the panel's config editor; open it from its own page.`,
        )
      } else {
        setSaveError(fmErrorText(err, "The file could not be saved."))
      }
    } finally {
      setBusy(false)
    }
  }

  const dirty = draft !== null && draft !== (conflict?.content ?? file?.content)

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <span className="font-mono text-sm break-all">{path}</span>
            {dirty && <Badge variant="outline">unsaved changes</Badge>}
          </DialogTitle>
          <DialogDescription>
            {savedAt !== null && !dirty
              ? "Saved."
              : "Changes are checked against the file on disk when you save."}
          </DialogDescription>
        </DialogHeader>

        {loadError ? (
          <p role="alert" className="text-destructive text-sm">
            {loadError}
          </p>
        ) : conflict !== null ? (
          <div className="flex flex-col gap-3">
            <p role="alert" className="text-destructive text-sm">
              The file changed on disk since it was opened. Your draft is kept below; the
              version now on disk follows.
            </p>
            <ConfigEditor
              label="The file as it is on disk now"
              value={conflict.content}
              onChange={() => {}}
              original={file?.content}
            />
            <p className="text-muted-foreground text-xs">
              Saving overwrites the disk copy with your draft. Closing keeps the disk copy and
              discards nothing — your draft is lost when the dialog closes.
            </p>
          </div>
        ) : file !== null ? (
          <ConfigEditor
            label={`Contents of ${path}`}
            value={draft ?? file.content}
            onChange={setDraft}
          />
        ) : (
          <SkeletonRows label="Loading the file…" />
        )}

        {saveError && (
          <p role="alert" className="text-destructive text-sm">
            {saveError}
          </p>
        )}

        <DialogFooter>
          {conflict !== null ? (
            <>
              <Button variant="outline" onClick={onClose}>
                Keep disk copy
              </Button>
              <Button disabled={busy} onClick={() => void save(conflict)}>
                {busy ? "Saving…" : "Overwrite with my draft"}
              </Button>
            </>
          ) : (
            <>
              <Button variant="outline" onClick={onClose}>
                Close
              </Button>
              <Button disabled={!dirty || busy || file === null} onClick={() => void save(null)}>
                {busy ? "Saving…" : "Save"}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
