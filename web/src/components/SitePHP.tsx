import { SkeletonRows } from "@/components/ui/skeleton"
import { useEffect, useId, useState } from "react"

import { ApplyError } from "@/components/ApplyError"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select } from "@/components/ui/select"
import { ApiError, type PoolMode, type PoolSettings, type SiteDetail } from "@/lib/api"
import { usePHP, useSaveSitePool, useSitePool, useSwitchPHP } from "@/lib/queries"

/** The PHP tab of a site: which version it runs on, and its pool. */
export function SitePHP({ detail }: { detail: SiteDetail }) {
  return (
    <div className="flex flex-col gap-8">
      <VersionSwitch detail={detail} />
      <PoolSection domain={detail.domain} />
    </div>
  )
}

function VersionSwitch({ detail }: { detail: SiteDetail }) {
  const id = useId()
  const php = usePHP()
  const change = useSwitchPHP(detail.domain)
  const [done, setDone] = useState<string | null>(null)

  const others = (php.data?.versions ?? [])
    .filter((v) => v.state === "ready" && v.version !== detail.php_version)
    .map((v) => v.version)
  const [target, setTarget] = useState("")
  const chosen = others.includes(target) ? target : (others[others.length - 1] ?? "")

  const feature = detail.features.find((f) => f.feature === "php")
  const detached = feature !== undefined && !feature.attached

  return (
    <section aria-labelledby={`${id}-heading`} className="flex flex-col gap-3">
      <h3 id={`${id}-heading`} className="text-sm font-semibold">
        PHP version
      </h3>
      <p className="text-sm">
        This site runs on PHP {detail.php_version}. Switching checks the new version's
        configuration first; if anything fails the site stays on PHP {detail.php_version}.
      </p>
      {detached ? (
        <p role="status" className="text-muted-foreground text-sm">
          Switched off: {feature.reason ?? "the site file no longer includes this control's file."}
        </p>
      ) : others.length === 0 ? (
        <p role="status" className="text-muted-foreground text-sm">
          No other PHP version is installed. Pull one on the PHP page to switch.
        </p>
      ) : (
        <div className="flex flex-wrap items-end gap-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-target`}>Switch to</Label>
            <Select
              id={`${id}-target`}
              className="w-40"
              value={chosen}
              onChange={(e) => {
                setTarget(e.target.value)
                setDone(null)
              }}
            >
              {others.map((v) => (
                <option key={v} value={v}>
                  PHP {v}
                </option>
              ))}
            </Select>
          </div>
          <Button
            disabled={change.isPending || chosen === ""}
            onClick={() => {
              setDone(null)
              change.mutate(chosen, { onSuccess: () => setDone(`Now running on PHP ${chosen}.`) })
            }}
          >
            {change.isPending ? "Switching…" : "Switch version"}
          </Button>
          <span className="text-muted-foreground text-xs" aria-live="polite">
            {done ?? ""}
          </span>
        </div>
      )}
      <ApplyError error={change.error} fallback="The version could not be switched." />
    </section>
  )
}

function PoolSection({ domain }: { domain: string }) {
  const { data, isPending, error } = useSitePool(domain)
  const save = useSaveSitePool(domain)

  return (
    <section aria-labelledby="pool-heading" className="flex flex-col gap-3">
      <h3 id="pool-heading" className="text-sm font-semibold">
        Worker pool
      </h3>
      {isPending ? (
        <SkeletonRows label="Loading the pool…" />
      ) : error ? (
        <p role="alert" className="text-destructive text-sm">
          {error instanceof ApiError ? error.detail : "The pool could not be loaded."}
        </p>
      ) : (
        <>
          <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1 text-sm">
            <dt className="text-muted-foreground">Pool file</dt>
            <dd className="font-mono text-xs break-all">{data.file}</dd>
            <dt className="text-muted-foreground">Socket</dt>
            <dd className="font-mono text-xs break-all">{data.socket}</dd>
          </dl>
          <PoolForm
            settings={data.settings}
            pending={save.isPending}
            error={save.error}
            onSave={(next, done) => save.mutate(next, { onSuccess: done })}
            onDiscard={() => save.reset()}
          />
        </>
      )}
    </section>
  )
}

const modes: { value: PoolMode; label: string; hint: string }[] = [
  { value: "dynamic", label: "Dynamic", hint: "Keeps a few workers ready and adds more under load." },
  { value: "ondemand", label: "On demand", hint: "Starts workers only when requests arrive. Uses the least memory." },
  { value: "static", label: "Static", hint: "Always runs the full number of workers." },
]

type NumberKey = Exclude<keyof PoolSettings, "pm">

/**
 * The pool form, without the request around it. Only the fields the chosen
 * mode uses are shown; the others keep their values for when the mode changes
 * back.
 */
export function PoolForm({
  settings,
  pending,
  error,
  onSave,
  onDiscard,
}: {
  settings: PoolSettings
  pending: boolean
  error: unknown
  /** `done` is called when the save went live. */
  onSave: (settings: PoolSettings, done: () => void) => void
  onDiscard?: () => void
}) {
  const id = useId()
  const [draft, setDraft] = useState(settings)
  const [saved, setSaved] = useState(false)

  useEffect(() => setDraft(settings), [settings])

  const dirty = JSON.stringify(draft) !== JSON.stringify(settings)
  const badField = error instanceof ApiError ? error.field : null

  const number = (key: NumberKey, label: string, hint: string) => (
    <div key={key} className="flex flex-col gap-1.5">
      <Label htmlFor={`${id}-${key}`}>{label}</Label>
      <Input
        id={`${id}-${key}`}
        type="number"
        min={0}
        value={String(draft[key])}
        onChange={(e) => {
          setDraft({ ...draft, [key]: Number.parseInt(e.target.value, 10) || 0 })
          setSaved(false)
        }}
        aria-invalid={badField === key}
        aria-describedby={`${id}-${key}-hint${badField === key ? ` ${id}-${key}-error` : ""}`}
      />
      <p id={`${id}-${key}-hint`} className="text-muted-foreground text-xs">
        {hint}
      </p>
      {badField === key && error instanceof ApiError && (
        <p id={`${id}-${key}-error`} role="alert" className="text-destructive text-xs">
          {error.detail.replace(`${key}: `, "")}
        </p>
      )}
    </div>
  )

  return (
    <form
      className="flex flex-col gap-5"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        onSave(draft, () => setSaved(true))
      }}
    >
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-pm`}>How workers are managed</Label>
        <Select
          id={`${id}-pm`}
          className="w-48"
          value={draft.pm}
          onChange={(e) => {
            setDraft({ ...draft, pm: e.target.value as PoolMode })
            setSaved(false)
          }}
          aria-describedby={`${id}-pm-hint`}
        >
          {modes.map((m) => (
            <option key={m.value} value={m.value}>
              {m.label}
            </option>
          ))}
        </Select>
        <p id={`${id}-pm-hint`} className="text-muted-foreground text-xs">
          {modes.find((m) => m.value === draft.pm)?.hint}
        </p>
      </div>

      <div className="grid gap-5 sm:grid-cols-2">
        {number("max_children", "Most workers", "The most requests PHP handles at once for this site.")}
        {draft.pm === "dynamic" && (
          <>
            {number("start_servers", "Workers at start", "Between the fewest and most idle workers.")}
            {number("min_spare_servers", "Fewest idle workers", "More are started when fewer are idle.")}
            {number("max_spare_servers", "Most idle workers", "Extra idle workers are stopped.")}
          </>
        )}
        {draft.pm === "ondemand" &&
          number("idle_timeout", "Idle seconds before a worker stops", "From 1 to 3600.")}
        {number("max_requests", "Requests before a worker restarts", "Limits memory leaks. 0 never restarts.")}
      </div>

      {(badField === null || badField === "pm") && (
        <ApplyError error={error} fallback="The pool could not be saved." />
      )}

      <div className="flex items-center gap-3">
        <Button type="submit" disabled={!dirty || pending}>
          {pending ? "Checking with PHP…" : "Save and reload"}
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
          {saved && !dirty ? "Saved. PHP reloaded." : ""}
        </span>
      </div>
    </form>
  )
}
