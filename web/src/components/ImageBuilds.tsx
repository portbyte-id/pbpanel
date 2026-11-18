import { SkeletonRows } from "@/components/ui/skeleton"
import { useId, useState } from "react"

import { ApplyError } from "@/components/ApplyError"
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
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { ApiError, type ImageBuild, type ImageBuildState, type PHPExtension } from "@/lib/api"
import { usePHPExtensions } from "@/lib/queries"

/** The rule the server applies to a PECL package name. */
export const PECL_NAME_PATTERN = /^[a-z][a-z0-9_]{1,39}$/

/** The most extensions one build may carry. */
export const MAX_EXTENSIONS = 32

/** An error sentence for a PECL package name the server would refuse, or null when it is fine. */
export function validatePeclName(raw: string): string | null {
  const name = raw.trim()
  if (name === "") return "Enter a PECL package name."
  if (!PECL_NAME_PATTERN.test(name)) {
    return "Use 2 to 40 lowercase letters, digits or underscores, starting with a letter."
  }
  return null
}

/** The badge text and colour for a build's state. */
export function describeBuild(state: ImageBuildState): {
  label: string
  variant: "secondary" | "outline" | "destructive"
} {
  switch (state) {
    case "ready":
      return { label: "ready", variant: "secondary" }
    case "building":
      return { label: "building", variant: "outline" }
    case "failed":
      return { label: "failed", variant: "destructive" }
    case "pruned":
      return { label: "pruned", variant: "outline" }
  }
}

/** The extensions a build carries, or the empty text when it carries none. */
export function ExtensionChips({ names, empty }: { names?: string[] | null; empty: string }) {
  if (!names || names.length === 0) return <span className="text-muted-foreground text-xs">{empty}</span>
  return (
    <span className="flex flex-wrap gap-1">
      {names.map((name) => (
        <Badge key={name} variant="outline" className="font-mono">
          {name}
        </Badge>
      ))}
    </span>
  )
}

export function formatBuildTime(iso: string): string {
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString()
}

/**
 * One row per build, newest first. Only a ready build that is not the one in use
 * can be activated, which is how a previous build is rolled back to.
 */
export function BuildHistory({
  builds,
  emptyText,
  showExtensions = true,
  onActivate,
  activating,
  error,
  fallback,
}: {
  builds: ImageBuild[]
  emptyText: string
  /** Hide the extension column for builds that carry none, such as nginx's. */
  showExtensions?: boolean
  onActivate?: (build: ImageBuild) => void
  activating?: boolean
  error: unknown
  fallback: string
}) {
  if (!builds || builds.length === 0) {
    return <p className="text-muted-foreground text-sm">{emptyText}</p>
  }
  return (
    <div className="flex flex-col gap-2">
      <ol aria-label="Build history" className="divide-border flex flex-col divide-y">
        {builds.map((build) => {
          const badge = describeBuild(build.state)
          const canActivate = onActivate !== undefined && build.state === "ready" && !build.current
          return (
            <li
              key={build.id}
              data-testid="build-row"
              className="flex flex-col gap-2 py-2 first:pt-0 last:pb-0 text-sm"
            >
              <div className="flex flex-wrap items-center gap-2">
                <time dateTime={build.created_at} className="text-xs">
                  {formatBuildTime(build.created_at)}
                </time>
                <Badge variant={badge.variant}>{badge.label}</Badge>
                {build.current && <Badge variant="default">serving</Badge>}
                {build.previous && <Badge variant="outline">previous</Badge>}
                <span className="font-mono text-muted-foreground text-xs">{build.id}</span>
                {showExtensions && (
                  <span className="flex-1">
                    <ExtensionChips names={build.extensions} empty="no extra extensions" />
                  </span>
                )}
                {canActivate && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={activating}
                    aria-label={`Activate build ${build.id}`}
                    onClick={() => onActivate(build)}
                  >
                    Activate
                  </Button>
                )}
              </div>
              {build.state === "failed" && build.output && (
                <pre className="bg-muted overflow-x-auto rounded-md p-2 font-mono text-xs whitespace-pre-wrap">
                  {build.output.trimEnd()}
                </pre>
              )}
            </li>
          )
        })}
      </ol>
      <ApplyError error={error} fallback={fallback} />
    </div>
  )
}

interface PickerEntry {
  name: string
  description: string
  packages: string[]
}

/**
 * The extension checkboxes and the box for a PECL package that is not in the
 * catalogue. Its state is the selection, so it is created fresh each time the
 * dialog opens with the current build's extensions ticked.
 */
export function ExtensionPicker({
  catalogue,
  initial,
  submitLabel,
  pendingLabel,
  pending,
  error,
  onSubmit,
  onCancel,
}: {
  catalogue: PHPExtension[]
  initial: string[]
  submitLabel: string
  pendingLabel: string
  pending: boolean
  error: unknown
  onSubmit: (extensions: string[]) => void
  onCancel: () => void
}) {
  const id = useId()
  const [checked, setChecked] = useState<string[]>(initial ?? [])
  const [custom, setCustom] = useState<string[]>(() =>
    (initial ?? []).filter((name) => !catalogue.some((e) => e.name === name)),
  )
  const [draft, setDraft] = useState("")
  const [peclError, setPeclError] = useState<string | null>(null)

  const atCap = checked.length >= MAX_EXTENSIONS

  const toggle = (name: string, on: boolean) => {
    setChecked((prev) => {
      if (!on) return prev.filter((n) => n !== name)
      if (prev.includes(name) || prev.length >= MAX_EXTENSIONS) return prev
      return [...prev, name]
    })
    if (!on) setCustom((prev) => prev.filter((n) => n !== name))
  }

  const addCustom = () => {
    const invalid = validatePeclName(draft)
    if (invalid) {
      setPeclError(invalid)
      return
    }
    const name = draft.trim()
    if (checked.includes(name)) {
      setPeclError("That package is already chosen.")
      return
    }
    if (atCap) {
      setPeclError(`At most ${MAX_EXTENSIONS} extensions in one build.`)
      return
    }
    if (!catalogue.some((e) => e.name === name)) setCustom((prev) => [...prev, name])
    setChecked((prev) => [...prev, name])
    setDraft("")
    setPeclError(null)
  }

  const bundled: PickerEntry[] = catalogue
    .filter((e) => e.kind === "bundled")
    .map((e) => ({ name: e.name, description: e.description, packages: e.packages }))
  const pecl: PickerEntry[] = [
    ...catalogue
      .filter((e) => e.kind === "pecl")
      .map((e) => ({ name: e.name, description: e.description, packages: e.packages })),
    ...custom.map((name) => ({ name, description: "Not in the catalogue.", packages: [] })),
  ]

  const group = (title: string, entries: PickerEntry[]) => (
    <fieldset className="flex flex-col gap-2">
      <legend className="mb-1 text-sm font-medium">{title}</legend>
      {entries.length === 0 && <p className="text-muted-foreground text-xs">None.</p>}
      {entries.map((entry) => {
        const on = checked.includes(entry.name)
        return (
          <label key={entry.name} className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              className="mt-1"
              checked={on}
              disabled={!on && atCap}
              onChange={(e) => toggle(entry.name, e.target.checked)}
            />
            <span className="flex flex-col">
              <span className="font-mono">{entry.name}</span>
              <span className="text-muted-foreground text-xs">
                {entry.description}
                {entry.packages.length > 0 && ` Needs ${entry.packages.join(", ")}.`}
              </span>
            </span>
          </label>
        )
      })}
    </fieldset>
  )

  return (
    <form
      className="flex flex-col gap-5"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        onSubmit([...checked].sort())
      }}
    >
      <p className="text-muted-foreground text-xs" aria-live="polite">
        {checked.length} of {MAX_EXTENSIONS} chosen.
      </p>
      <div className="grid gap-5 sm:grid-cols-2">
        {group("Bundled", bundled)}
        {group("PECL", pecl)}
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-pecl`}>Another PECL package</Label>
        <div className="flex gap-2">
          <Input
            id={`${id}-pecl`}
            value={draft}
            spellCheck={false}
            placeholder="for example igbinary"
            aria-invalid={peclError !== null}
            aria-describedby={`${id}-pecl-hint`}
            onChange={(e) => {
              setDraft(e.target.value)
              setPeclError(null)
            }}
            onKeyDown={(e) => {
              // The box sits inside the form: Enter adds the package, it does not submit.
              if (e.key === "Enter") {
                e.preventDefault()
                addCustom()
              }
            }}
          />
          <Button type="button" variant="outline" disabled={draft.trim() === ""} onClick={addCustom}>
            Add
          </Button>
        </div>
        <p id={`${id}-pecl-hint`} className="text-muted-foreground text-xs">
          2 to 40 characters: lowercase letters, digits and underscores, starting with a letter.
        </p>
        {peclError && (
          <p role="alert" className="text-destructive text-xs">
            {peclError}
          </p>
        )}
      </div>

      <ApplyError error={error} fallback="The request could not be started." />

      <DialogFooter className="bg-background sticky -bottom-4 -mx-4 -mb-4 border-t p-4">
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={pending}>
          {pending ? pendingLabel : submitLabel}
        </Button>
      </DialogFooter>
    </form>
  )
}

/** Loads the catalogue only once the dialog is open, so a closed page makes no request. */
function CataloguePicker(props: Omit<Parameters<typeof ExtensionPicker>[0], "catalogue">) {
  const catalogue = usePHPExtensions()
  if (catalogue.isPending) {
    return <SkeletonRows label="Loading extensions…" />
  }
  if (catalogue.error || !catalogue.data) {
    return (
      <p role="alert" className="text-destructive text-sm">
        {catalogue.error instanceof ApiError
          ? catalogue.error.detail
          : "The extension list could not be loaded."}
      </p>
    )
  }
  return <ExtensionPicker {...props} catalogue={catalogue.data} />
}

/** The extension picker in a dialog. Closing it discards the selection. */
export function ExtensionDialog({
  open,
  onOpenChange,
  title,
  description,
  initial,
  submitLabel,
  pendingLabel,
  pending,
  error,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  initial: string[]
  submitLabel: string
  pendingLabel: string
  pending: boolean
  error: unknown
  onSubmit: (extensions: string[]) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <CataloguePicker
          initial={initial}
          submitLabel={submitLabel}
          pendingLabel={pendingLabel}
          pending={pending}
          error={error}
          onSubmit={onSubmit}
          onCancel={() => onOpenChange(false)}
        />
      </DialogContent>
    </Dialog>
  )
}
