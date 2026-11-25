import { canAutoFocus } from "@/lib/utils"
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
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { api } from "@/lib/api"
import { fmErrorText } from "@/components/fm/FMShared"

export type PromptKind = "mkdir" | "create" | "rename"

/**
 * A name prompt for the operations that only need one: new folder, new file,
 * rename. The server validates the name; its sentence is shown as it came.
 */
export function NameDialog({
  kind,
  path,
  initialName,
  onDone,
  onOpenChange,
}: {
  kind: PromptKind
  /** The directory the name lands in, or the entry being renamed. */
  path: string
  /** Rename only: the name to prefill. */
  initialName?: string
  onDone: () => void
  onOpenChange: (open: boolean) => void
}) {
  const [name, setName] = useState(initialName ?? "")
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    setName(initialName ?? "")
  }, [initialName])

  const title =
    kind === "mkdir" ? "New folder" : kind === "create" ? "New file" : "Rename"

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      if (kind === "rename") {
        await api.fmRename(path, name)
      } else {
        const target = `${path === "/" ? "" : path}/${name}`
        if (kind === "mkdir") await api.fmMkdir(target)
        else await api.fmCreateFile(target)
      }
      onDone()
      onOpenChange(false)
    } catch (err) {
      setError(fmErrorText(err, "The operation failed."))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription className="font-mono text-xs">{path}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="fm-name">Name</Label>
            <Input
              id="fm-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus={canAutoFocus()}
              spellCheck={false}
            />
          </div>
          {error && (
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={name === "" || busy}>
              {busy ? "Working…" : title}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * The typed confirmation for a destructive action. The server refuses a delete
 * of a folder — and a permanent delete of anything — unless `confirm` equals
 * the item's base name, so the dialog asks for exactly that before sending.
 */
export function ConfirmDialog({
  title,
  description,
  /** The text the operator must type; empty makes Confirm always enabled. */
  confirmText,
  actionLabel,
  busy,
  error,
  onConfirm,
  onOpenChange,
  children,
}: {
  title: string
  description: string
  confirmText?: string
  actionLabel: string
  busy: boolean
  error: string | null
  onConfirm: () => void
  onOpenChange: (open: boolean) => void
  /** Extra controls under the description, such as an option checkbox. */
  children?: React.ReactNode
}) {
  const [typed, setTyped] = useState("")
  const locked = confirmText !== undefined && typed !== confirmText

  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {confirmText !== undefined && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="fm-confirm">
              Type <span className="font-mono">{confirmText}</span> to confirm
            </Label>
            <Input
              id="fm-confirm"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoFocus={canAutoFocus()}
              spellCheck={false}
              autoComplete="off"
            />
          </div>
        )}
        {children}
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="destructive"
            disabled={locked || busy}
            onClick={onConfirm}
          >
            {busy ? "Working…" : actionLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/**
 * Asks where to unpack an archive, defaulting to a folder named after it beside
 * the archive. The extract itself runs as a job; the caller opens the drawer.
 */
export function ExtractDialog({
  archive,
  parent,
  defaultName,
  busy,
  error,
  onConfirm,
  onOpenChange,
}: {
  /** The archive's absolute path, shown as a hint. */
  archive: string
  /** The folder the destination name is relative to. */
  parent: string
  defaultName: string
  busy: boolean
  error: string | null
  onConfirm: (dest: string) => void
  onOpenChange: (open: boolean) => void
}) {
  const [name, setName] = useState(defaultName)
  const dest = parent === "/" ? `/${name}` : `${parent}/${name}`

  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Extract archive</DialogTitle>
          <DialogDescription className="font-mono text-xs break-all">{archive}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            onConfirm(dest)
          }}
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="fm-extract-name">Extract into a folder named</Label>
            <Input
              id="fm-extract-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus={canAutoFocus()}
              spellCheck={false}
            />
            <p className="text-muted-foreground text-xs font-mono break-all">{dest}</p>
          </div>
          {error && (
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={name === "" || busy}>
              {busy ? "Starting…" : "Extract"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
