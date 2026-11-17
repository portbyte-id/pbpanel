import { SkeletonRows } from "@/components/ui/skeleton"
import { useEffect, useMemo, useState } from "react"

import { SettingField } from "@/components/SettingsField"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ApiError, type SettingKey, type SettingWrite } from "@/lib/api"
import { useSaveSettings, useSettings } from "@/lib/queries"

/**
 * The Settings page, generated from the registry.
 *
 * Nothing here knows the name of a setting. The page renders whatever groups
 * and keys `GET /api/settings` returns, in the registry's order, and sends back
 * only the keys whose draft differs from the value in effect.
 */
export function SettingsPage() {
  const { data, isPending, error: loadError } = useSettings()
  const save = useSaveSettings()

  const [draft, setDraft] = useState<Record<string, string>>({})
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [savedAt, setSavedAt] = useState<number | null>(null)
  const [tab, setTab] = useState<string | null>(null)

  const effective = useMemo(() => {
    const out: Record<string, string> = {}
    for (const group of data?.groups ?? []) {
      for (const key of group.keys) out[key.name] = key.value
    }
    return out
  }, [data])

  // A save replaces the server state, so the draft follows it. This also runs
  // on first load, which is what fills the form.
  useEffect(() => {
    setDraft(effective)
  }, [effective])

  const dirty = useMemo(
    () =>
      Object.entries(draft)
        .filter(([name, value]) => effective[name] !== undefined && effective[name] !== value)
        .map(([name, value]) => ({ name, value })),
    [draft, effective],
  )

  const submit = () => {
    if (dirty.length === 0) return
    setFieldErrors({})
    save.mutate(dirty as SettingWrite[], {
      onSuccess: () => setSavedAt(Date.now()),
      onError: (err) => {
        const mapped = mapSettingError(err, dirty)
        setFieldErrors(mapped.fieldErrors)
        // The failing key may sit in a tab that is not showing: go to it.
        const bad = Object.keys(mapped.fieldErrors)[0]
        const owner = data?.groups.find((g) => g.keys.some((k) => k.name === bad))
        if (owner) setTab(owner.name)
      },
    })
  }

  if (isPending) {
    return <SkeletonRows label="Loading settings…" />
  }
  if (loadError) {
    return (
      <p role="alert" className="text-destructive text-sm">
        {loadError instanceof ApiError ? loadError.detail : "Settings could not be loaded."}
      </p>
    )
  }

  // An error the API did not attribute to one key is shown once, above the form.
  const generalError =
    save.error && Object.keys(fieldErrors).length === 0
      ? save.error instanceof ApiError
        ? save.error.detail
        : "The settings could not be saved."
      : null

  return (
    <div className="flex max-w-3xl flex-col gap-4">
      {(data?.groups ?? []).length > 0 && (
        <Tabs value={tab ?? data?.groups[0]?.name} onValueChange={setTab} className="gap-3">
          <TabsList className="flex-wrap">
            {(data?.groups ?? []).map((group) => {
              const unsaved = group.keys.some((k) => dirty.some((d) => d.name === k.name))
              return (
                <TabsTrigger key={group.name} value={group.name}>
                  {group.name}
                  {unsaved && (
                    <span
                      className="bg-warning ml-1.5 inline-block size-1.5 rounded-full"
                      role="img"
                      aria-label="unsaved changes"
                    />
                  )}
                </TabsTrigger>
              )
            })}
          </TabsList>
          {(data?.groups ?? []).map((group) => (
            <TabsContent key={group.name} value={group.name}>
              <Card>
                <CardHeader>
                  <CardTitle>{group.name}</CardTitle>
                </CardHeader>
                <CardContent className="divide-border divide-y">
                  {group.keys.map((key: SettingKey) => (
                    <SettingField
                      key={key.name}
                      setting={key}
                      value={draft[key.name] ?? key.value}
                      error={fieldErrors[key.name]}
                      onChange={(value) => {
                        setDraft((prev) => ({ ...prev, [key.name]: value }))
                        setSavedAt(null)
                      }}
                    />
                  ))}
                </CardContent>
              </Card>
            </TabsContent>
          ))}
        </Tabs>
      )}

      {generalError && (
        <p role="alert" className="text-destructive text-sm">
          {generalError}
        </p>
      )}

      <div className="flex items-center gap-3">
        <Button onClick={submit} disabled={dirty.length === 0 || save.isPending}>
          {save.isPending ? "Saving…" : "Save changes"}
        </Button>
        <Button
          variant="outline"
          onClick={() => {
            setDraft(effective)
            setFieldErrors({})
            save.reset()
            setSavedAt(null)
          }}
          disabled={dirty.length === 0 && Object.keys(fieldErrors).length === 0}
        >
          Discard
        </Button>
        <span className="text-muted-foreground text-xs" aria-live="polite">
          {savedAt !== null
            ? "Saved."
            : dirty.length > 0
              ? `${dirty.length} change${dirty.length === 1 ? "" : "s"} not saved`
              : ""}
        </span>
      </div>
    </div>
  )
}

/**
 * Maps a save failure to the row it belongs to.
 *
 * The server rejects the first bad value and says which one: the detail reads
 * `settings[3] (login.max_attempts): <reason>`. The index is in the list this
 * page sent, so it names the key the operator has to fix; anything that does
 * not match that shape stays a form-level error.
 */
export function mapSettingError(
  err: unknown,
  writes: SettingWrite[],
): { fieldErrors: Record<string, string> } {
  const fieldErrors: Record<string, string> = {}
  if (!(err instanceof ApiError) || err.code !== "invalid_setting") {
    return { fieldErrors }
  }

  const match = /^settings\[(\d+)\] \(([^)]+)\): (.*)$/.exec(err.detail)
  if (!match) return { fieldErrors }

  const index = Number.parseInt(match[1], 10)
  const name = writes[index]?.name ?? match[2]
  fieldErrors[name] = match[3]
  return { fieldErrors }
}
