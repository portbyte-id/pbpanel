import { useEffect, useId, useState } from "react"

import { ApplyError } from "@/components/ApplyError"
import { ConfigFilePanel } from "@/components/ConfigFilePanel"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ApiError, type PHPSettings, type PHPVersion } from "@/lib/api"
import { usePHP, usePHPSettings, usePullPHP, useSavePHPSettings } from "@/lib/queries"

/** A badge and a sentence for one version, from its image state and its container. */
export function describeVersion(v: PHPVersion): { badge: string; ok: boolean; text: string } {
  switch (v.state) {
    case "pulling":
      return { badge: "pulling", ok: true, text: "The image is being downloaded." }
    case "failed":
      return { badge: "failed", ok: false, text: "The last download failed." }
    case "absent":
      return {
        badge: "not installed",
        ok: true,
        text: v.supported ? "Pull it to use it for sites." : "No longer offered.",
      }
  }
  const sites = `${v.sites.length} site${v.sites.length === 1 ? "" : "s"}`
  if (v.container.error) return { badge: "unknown", ok: false, text: v.container.error }
  if (v.container.running) return { badge: "running", ok: true, text: `Serving ${sites}.` }
  if (v.sites.length === 0) {
    return { badge: "installed", ok: true, text: "It starts when the first site uses it." }
  }
  return { badge: "stopped", ok: false, text: `Not running, but ${sites} need it.` }
}

/** The PHP versions: which are installed, and each one's settings and php.ini. */
export function PHPPage() {
  const { data, isPending, error } = usePHP()
  const pull = usePullPHP()
  const [selected, setSelected] = useState<string | null>(null)

  if (isPending) {
    return <p className="text-muted-foreground text-sm">Loading PHP versions…</p>
  }
  if (error) {
    return (
      <p role="alert" className="text-destructive text-sm">
        {error instanceof ApiError ? error.detail : "The PHP versions could not be loaded."}
      </p>
    )
  }

  const ready = data.versions.filter((v) => v.state === "ready")
  const current = ready.find((v) => v.version === selected) ?? ready[ready.length - 1]
  const failedSteps = data.reconcile.filter((s) => !s.ok)

  return (
    <div className="flex max-w-4xl flex-col gap-6">
      {failedSteps.length > 0 && (
        <div role="alert" className="border-destructive/40 bg-destructive/5 rounded-md border p-3">
          <p className="text-destructive text-sm font-medium">
            The panel could not bring PHP up when it started.
          </p>
          <ul className="mt-2 flex flex-col gap-1 text-xs">
            {failedSteps.map((s) => (
              <li key={s.version}>
                <span className="font-medium">PHP {s.version}:</span> {s.detail}
              </li>
            ))}
          </ul>
        </div>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Versions</CardTitle>
        </CardHeader>
        <CardContent>
          <ul className="divide-border flex flex-col divide-y">
            {data.versions.map((v) => {
              const d = describeVersion(v)
              const canPull = v.supported && (v.state === "absent" || v.state === "failed")
              return (
                <li key={v.version} className="flex flex-col gap-2 py-3 first:pt-0 last:pb-0">
                  <div className="flex flex-wrap items-center gap-3 text-sm">
                    <span className="w-16 font-medium">PHP {v.version}</span>
                    <Badge variant={d.ok ? "secondary" : "destructive"}>{d.badge}</Badge>
                    <span className="text-muted-foreground flex-1">{d.text}</span>
                    {canPull && (
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={pull.isPending}
                        onClick={() => pull.mutate(v.version)}
                      >
                        {v.state === "failed" ? "Try again" : "Pull"}
                      </Button>
                    )}
                    {v.state === "ready" && (
                      <Button
                        size="sm"
                        variant={current?.version === v.version ? "default" : "outline"}
                        aria-pressed={current?.version === v.version}
                        onClick={() => setSelected(v.version)}
                      >
                        Configure
                      </Button>
                    )}
                  </div>
                  {v.state === "failed" && v.output && (
                    <pre className="bg-muted overflow-x-auto rounded-md p-2 font-mono text-xs whitespace-pre-wrap">
                      {v.output.trimEnd()}
                    </pre>
                  )}
                </li>
              )
            })}
          </ul>
          <ApplyError error={pull.error} fallback="The pull could not be started." />
          <p className="text-muted-foreground mt-4 text-xs">
            Versions run on the official php:&lt;version&gt;-fpm images. The list is the "PHP
            versions" setting. A pull's full output is in Jobs.
          </p>
        </CardContent>
      </Card>

      {current && (
        // Keyed on the version: each has its own form state and editor.
        <VersionConfig key={current.version} version={current.version} />
      )}
    </div>
  )
}

function VersionConfig({ version }: { version: string }) {
  const { data, isPending, error } = usePHPSettings(version)
  const save = useSavePHPSettings(version)

  return (
    <section aria-labelledby="php-config-heading" className="flex flex-col gap-3">
      <h2 id="php-config-heading" className="text-lg font-semibold tracking-tight">
        PHP {version}
      </h2>
      <Tabs defaultValue="settings">
        <TabsList>
          <TabsTrigger value="settings">Settings</TabsTrigger>
          <TabsTrigger value="ini">php.ini</TabsTrigger>
        </TabsList>
        <TabsContent value="settings">
          {isPending ? (
            <p className="text-muted-foreground text-sm">Loading settings…</p>
          ) : error ? (
            <p role="alert" className="text-destructive text-sm">
              {error instanceof ApiError ? error.detail : "The settings could not be loaded."}
            </p>
          ) : (
            <PHPSettingsForm
              version={version}
              settings={data}
              pending={save.isPending}
              error={save.error}
              onSave={(next, done) => save.mutate(next, { onSuccess: done })}
              onDiscard={() => save.reset()}
            />
          )}
        </TabsContent>
        <TabsContent value="ini">
          <ConfigFilePanel source={{ kind: "php-ini", version }} />
        </TabsContent>
      </Tabs>
    </section>
  )
}

const sizeFields: { key: "memory_limit" | "upload_max_filesize" | "post_max_size"; label: string; hint: string }[] = [
  { key: "memory_limit", label: "Memory per request", hint: "For example 256M. -1 removes the limit." },
  { key: "upload_max_filesize", label: "Largest uploaded file", hint: "For example 64M." },
  {
    key: "post_max_size",
    label: "Largest request body",
    hint: "At least the largest uploaded file, or uploads of that size fail.",
  },
]

/**
 * The php.ini form of one version, without the request around it. These values
 * are written to a file PHP reads last, so they win over php.ini.
 */
export function PHPSettingsForm({
  version,
  settings,
  pending,
  error,
  onSave,
  onDiscard,
}: {
  version: string
  settings: PHPSettings
  pending: boolean
  error: unknown
  /** `done` is called when the save went live. */
  onSave: (settings: PHPSettings, done: () => void) => void
  onDiscard?: () => void
}) {
  const id = useId()
  const [draft, setDraft] = useState(settings)
  const [saved, setSaved] = useState(false)

  // The form follows the server: on first load and after every save.
  useEffect(() => setDraft(settings), [settings])

  const set = <K extends keyof PHPSettings>(key: K, value: PHPSettings[K]) => {
    setDraft({ ...draft, [key]: value })
    setSaved(false)
  }
  const dirty = JSON.stringify(draft) !== JSON.stringify(settings)
  const badField = error instanceof ApiError ? error.field : null
  const fieldError = (key: keyof PHPSettings) =>
    badField === key && error instanceof ApiError ? (
      <p id={`${id}-${key}-error`} role="alert" className="text-destructive text-xs">
        {error.detail.replace(`${key}: `, "")}
      </p>
    ) : null
  const describedBy = (key: keyof PHPSettings) =>
    `${id}-${key}-hint${badField === key ? ` ${id}-${key}-error` : ""}`

  return (
    <form
      className="flex flex-col gap-5"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        onSave(draft, () => setSaved(true))
      }}
    >
      <div className="grid gap-5 sm:grid-cols-2">
        {sizeFields.map(({ key, label, hint }) => (
          <div key={key} className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-${key}`}>{label}</Label>
            <Input
              id={`${id}-${key}`}
              value={draft[key]}
              onChange={(e) => set(key, e.target.value.trim())}
              spellCheck={false}
              aria-invalid={badField === key}
              aria-describedby={describedBy(key)}
            />
            <p id={`${id}-${key}-hint`} className="text-muted-foreground text-xs">
              {hint}
            </p>
            {fieldError(key)}
          </div>
        ))}
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-max_execution_time`}>Longest script run, in seconds</Label>
          <Input
            id={`${id}-max_execution_time`}
            type="number"
            min={0}
            max={86400}
            value={String(draft.max_execution_time)}
            onChange={(e) => set("max_execution_time", Number.parseInt(e.target.value, 10) || 0)}
            aria-invalid={badField === "max_execution_time"}
            aria-describedby={describedBy("max_execution_time")}
          />
          <p id={`${id}-max_execution_time-hint`} className="text-muted-foreground text-xs">
            0 removes the limit.
          </p>
          {fieldError("max_execution_time")}
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-date_timezone`}>Timezone</Label>
          <Input
            id={`${id}-date_timezone`}
            value={draft.date_timezone}
            onChange={(e) => set("date_timezone", e.target.value.trim())}
            spellCheck={false}
            aria-invalid={badField === "date_timezone"}
            aria-describedby={describedBy("date_timezone")}
          />
          <p id={`${id}-date_timezone-hint`} className="text-muted-foreground text-xs">
            A name such as UTC or Europe/Rome.
          </p>
          {fieldError("date_timezone")}
        </div>
      </div>

      {badField === null && <ApplyError error={error} fallback="The settings could not be saved." />}

      <div className="flex items-center gap-3">
        <Button type="submit" disabled={!dirty || pending}>
          {pending ? `Checking with PHP ${version}…` : "Save and reload"}
        </Button>
        <Button
          type="button"
          variant="outline"
          disabled={!dirty}
          onClick={() => {
            setDraft(settings)
            onDiscard?.()
          }}
        >
          Discard
        </Button>
        <span className="text-muted-foreground text-xs" aria-live="polite">
          {saved && !dirty ? `Saved. PHP ${version} reloaded.` : ""}
        </span>
      </div>
    </form>
  )
}
