import { SkeletonRows } from "@/components/ui/skeleton"
import { useEffect, useState } from "react"

import { ConfirmDialog } from "@/components/fm/FMDialogs"
import { fmErrorText, formatSize } from "@/components/fm/FMShared"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { api, type FMTrashItem } from "@/lib/api"

/**
 * The recycle bin: list, restore, permanent delete.
 *
 * A permanent delete always asks for the item's name typed, because the server
 * refuses the request without it and the action cannot be undone. A restore of
 * a large item answers 202 with a job; the drawer shows its output.
 */
export function TrashPanel({ onChanged }: { onChanged: () => void }) {
  const [items, setItems] = useState<FMTrashItem[] | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [purging, setPurging] = useState<FMTrashItem | null>(null)
  const [busy, setBusy] = useState(false)

  const reload = () => {
    api
      .fmTrash()
      .then((list) => {
        setItems(list)
        setLoadError(null)
      })
      .catch((err) => {
        setLoadError(fmErrorText(err, "The trash could not be loaded."))
      })
  }

  useEffect(reload, [])

  const restore = async (item: FMTrashItem) => {
    setBusy(true)
    setError(null)
    try {
      await api.fmRestore(item.id)
      reload()
      onChanged()
    } catch (err) {
      setError(fmErrorText(err, "The item could not be restored."))
    } finally {
      setBusy(false)
    }
  }

  const purge = async (item: FMTrashItem) => {
    setBusy(true)
    setError(null)
    try {
      await api.fmPurge(item.id, item.name)
      setPurging(null)
      reload()
      onChanged()
    } catch (err) {
      setError(fmErrorText(err, "The item could not be deleted."))
      setPurging(null)
    } finally {
      setBusy(false)
    }
  }

  if (loadError) {
    return (
      <p role="alert" className="text-destructive text-sm">
        {loadError}
      </p>
    )
  }
  if (items === null) {
    return <SkeletonRows label="Loading the trash…" />
  }

  return (
    <div className="flex flex-col gap-3">
      <p className="text-muted-foreground text-sm">
        {items.length === 0
          ? "The trash is empty. Deleted files wait here for 30 days."
          : `${items.length} item${items.length === 1 ? "" : "s"} in the trash.`}
      </p>

      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}

      {items.length > 0 && (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-sm">
            <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
              <tr>
                <th scope="col" className="px-3 py-2 font-medium">Name</th>
                <th scope="col" className="px-3 py-2 font-medium">Original location</th>
                <th scope="col" className="px-3 py-2 font-medium">Size</th>
                <th scope="col" className="px-3 py-2 font-medium">Deleted</th>
                <th scope="col" className="px-3 py-2 font-medium">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {items.map((item) => (
                <tr key={item.id}>
                  <td className="px-3 py-2 font-medium">
                    <span className="flex items-center gap-2">
                      {item.name}
                      {item.is_dir && <Badge variant="secondary">folder</Badge>}
                    </span>
                  </td>
                  <td className="text-muted-foreground px-3 py-2 font-mono text-xs break-all">
                    {item.orig_path}
                  </td>
                  <td className="px-3 py-2">{formatSize(item.size, item.is_dir)}</td>
                  <td className="px-3 py-2 whitespace-nowrap">
                    {new Date(item.deleted_at).toLocaleString()}
                  </td>
                  <td className="px-3 py-2">
                    <div className="flex justify-end gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={busy}
                        onClick={() => void restore(item)}
                        aria-label={`Restore ${item.name}`}
                      >
                        Restore
                      </Button>
                      <Button
                        size="sm"
                        variant="destructive"
                        disabled={busy}
                        onClick={() => setPurging(item)}
                        aria-label={`Delete ${item.name} permanently`}
                      >
                        Delete forever
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {purging !== null && (
        <ConfirmDialog
          title="Delete permanently"
          description={`${purging.name} is removed for good. This cannot be undone.`}
          confirmText={purging.name}
          actionLabel="Delete forever"
          busy={busy}
          error={null}
          onConfirm={() => void purge(purging)}
          onOpenChange={(open) => !open && setPurging(null)}
        />
      )}
    </div>
  )
}
