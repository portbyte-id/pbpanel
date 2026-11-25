import { SkeletonRows } from "@/components/ui/skeleton"
import { useState } from "react"

import { ApplyError, ApplyWarnings } from "@/components/ApplyError"
import { ConfigEditor } from "@/components/ConfigEditor"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { TablePagination, TableToolbar, usePagedRows } from "@/components/ui/table-controls"
import { api, ApiError, type ConfigVersion } from "@/lib/api"
import { matchesQuery } from "@/lib/table"
import {
  type ConfigSource,
  configService,
  useConfigFile,
  useConfigHistory,
  useRestoreConfigVersion,
  useSaveConfigFile,
} from "@/lib/queries"

/** The saved versions of a file, newest first, with search and paging. */
function HistoryTable({
  versions,
  confirmRestore,
  restorePending,
  onLoad,
  onAskRestore,
  onRestore,
}: {
  versions: ConfigVersion[]
  confirmRestore: number | null
  restorePending: boolean
  onLoad: (v: ConfigVersion) => void
  onAskRestore: (version: number) => void
  onRestore: (version: number) => void
}) {
  const [search, setSearch] = useState("")
  const filtered = versions.filter((v) =>
    matchesQuery([`v${v.version}`, v.version, v.reason, new Date(v.created_at).toLocaleString()], search),
  )
  const pager = usePagedRows(filtered, search)
  return (
    <>
      <TableToolbar
        search={search}
        onSearchChange={setSearch}
        searchLabel="Search versions"
        placeholder="Search by version or change…"
      />
      {filtered.length === 0 ? (
        <p className="text-muted-foreground text-sm">No version matches the search.</p>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-sm">
            <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
              <tr>
                <th scope="col" className="px-3 py-1.5 font-medium">Version</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Saved</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Change</th>
                <th scope="col" className="px-3 py-1.5 font-medium">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {pager.rows.map((v) => (
                <tr key={v.version}>
                  <td className="px-3 py-1.5 font-mono text-xs">v{v.version}</td>
                  <td className="px-3 py-1.5 whitespace-nowrap">
                    {new Date(v.created_at).toLocaleString()}
                  </td>
                  <td className="px-3 py-1.5">{v.reason || "—"}</td>
                  <td className="px-3 py-1.5">
                    <div className="flex justify-end gap-2">
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => onLoad(v)}
                        aria-label={`Load version ${v.version} into the editor`}
                      >
                        Load into editor
                      </Button>
                      {confirmRestore === v.version ? (
                        <Button
                          size="sm"
                          variant="destructive"
                          disabled={restorePending}
                          onClick={() => onRestore(v.version)}
                        >
                          Confirm restore v{v.version}
                        </Button>
                      ) : (
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={restorePending}
                          onClick={() => onAskRestore(v.version)}
                          aria-label={`Restore version ${v.version}`}
                        >
                          Restore
                        </Button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <TablePagination pager={pager} />
    </>
  )
}

/**
 * The raw editor for a file the operator owns: a site file or nginx.conf.
 *
 * Saving is the validation. The server writes the file, runs `nginx -t`, and
 * either reloads nginx or puts the old file back and returns nginx's output,
 * which is shown as it came. The draft is kept after a rejection so the fix is
 * one edit away, and the file on the server is exactly what it was.
 */
export function ConfigFilePanel({ source }: { source: ConfigSource }) {
  const file = useConfigFile(source)
  const save = useSaveConfigFile(source)
  const history = useConfigHistory(file.data?.history_file)
  const restore = useRestoreConfigVersion(source, file.data?.history_file)

  // null means "what the server has"; a string is an unsaved draft.
  const [draft, setDraft] = useState<string | null>(null)
  const [showDiff, setShowDiff] = useState(false)
  const [saved, setSaved] = useState<string | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [confirmRestore, setConfirmRestore] = useState<number | null>(null)
  const service = configService(source)

  if (file.isPending) {
    return <SkeletonRows label="Loading the file…" />
  }
  if (file.error) {
    return (
      <p role="alert" className="text-destructive text-sm">
        {file.error instanceof ApiError ? file.error.detail : "The file could not be loaded."}
      </p>
    )
  }

  const current = file.data.content
  const text = draft ?? current
  const dirty = draft !== null && draft !== current

  const reset = () => {
    save.reset()
    restore.reset()
    setSaved(null)
    setLoadError(null)
  }

  const submit = () => {
    reset()
    save.mutate(text, {
      onSuccess: () => {
        setDraft(null)
        setSaved(`Saved. ${service} accepted the file and reloaded.`)
      },
    })
  }

  const loadVersion = async (v: ConfigVersion) => {
    reset()
    try {
      setDraft(await api.configVersion(file.data.history_file, v.version))
      setShowDiff(true)
    } catch (err) {
      setLoadError(err instanceof ApiError ? err.detail : "That version could not be loaded.")
    }
  }

  const doRestore = (version: number) => {
    reset()
    setConfirmRestore(null)
    restore.mutate(version, {
      onSuccess: () => {
        setDraft(null)
        setSaved(`Version ${version} restored. ${service} accepted it and reloaded.`)
      },
    })
  }

  const links = file.data.missing_links ?? []

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <code translate="no" className="text-muted-foreground text-xs">{file.data.path}</code>
        {dirty && <Badge variant="outline">unsaved changes</Badge>}
      </div>

      {file.data.missing && (
        <p role="status" className="text-muted-foreground text-sm">
          This file does not exist on disk. Saving creates it; "Regenerate site file" on the
          Overview tab writes the panel's template.
        </p>
      )}
      {links.length > 0 && (
        <p role="status" className="border-border rounded-md border p-3 text-sm">
          This file no longer includes the panel's file for {links.join(", ")}, so the matching
          controls have no effect until the <code translate="no">include</code> line is back.
        </p>
      )}

      <ConfigEditor
        label={`Contents of ${file.data.path}`}
        value={text}
        onChange={(next) => {
          setDraft(next)
          setSaved(null)
        }}
        original={showDiff ? current : undefined}
        language={source.kind === "php-ini" ? "ini" : "nginx"}
      />

      <div className="flex flex-wrap items-center gap-3">
        <Button onClick={submit} disabled={!dirty || save.isPending}>
          {save.isPending ? `Checking with ${service}…` : "Save and reload"}
        </Button>
        <Button
          variant="outline"
          disabled={!dirty}
          onClick={() => {
            setDraft(null)
            reset()
          }}
        >
          Discard
        </Button>
        <Button
          variant="ghost"
          aria-pressed={showDiff}
          onClick={() => setShowDiff((on) => !on)}
        >
          {showDiff ? "Hide changes" : "Show changes"}
        </Button>
        <span className="text-muted-foreground text-xs" aria-live="polite">
          {saved ?? ""}
        </span>
      </div>

      <ApplyError error={save.error} fallback="The file could not be saved." />
      <ApplyWarnings warnings={save.data?.warnings} />

      <section aria-labelledby="history-heading" className="flex flex-col gap-2">
        <h3 id="history-heading" className="text-sm font-semibold">
          History
        </h3>
        <ApplyError error={restore.error} fallback="The version could not be restored." />
        {loadError && (
          <p role="alert" className="text-destructive text-sm">
            {loadError}
          </p>
        )}
        {history.isPending ? (
          <SkeletonRows label="Loading history…" />
        ) : (history.data ?? []).length === 0 ? (
          <p className="text-muted-foreground text-sm">
            No earlier versions yet. Every saved change is kept here, up to the last ten.
          </p>
        ) : (
          <HistoryTable
            versions={history.data ?? []}
            confirmRestore={confirmRestore}
            restorePending={restore.isPending}
            onLoad={(v) => void loadVersion(v)}
            onAskRestore={setConfirmRestore}
            onRestore={doRestore}
          />
        )}
      </section>
    </div>
  )
}
