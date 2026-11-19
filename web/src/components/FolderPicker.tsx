import { useEffect, useState } from "react"

import { PathPicker } from "@/components/PathPicker"
import { api } from "@/lib/api"

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
    <PathPicker
      open={open}
      onOpenChange={onOpenChange}
      start={start}
      roots={roots}
      title="Choose a folder"
      description={
        <>
          Open a folder, then choose it. It must be inside {(roots ?? [SITES_ROOT]).join(" or ")}.
        </>
      }
      errorText={(path) =>
        path === SITES_ROOT
          ? `${SITES_ROOT} does not exist yet. Close this and type a folder name instead; it will be created.`
          : null
      }
      onPick={onPick}
    />
  )
}
