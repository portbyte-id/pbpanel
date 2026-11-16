import { useEffect, useId, useState } from "react"

import { ApplyError } from "@/components/ApplyError"
import { ConfigFilePanel } from "@/components/ConfigFilePanel"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ApiError, type NginxServiceStatus, type NginxSettings } from "@/lib/api"
import { useNginxService, useNginxSettings, useSaveNginxSettings } from "@/lib/queries"

/** nginx as a whole: whether it is running, the common settings, and nginx.conf. */
export function NginxPage() {
  return (
    <Tabs defaultValue="settings" className="max-w-4xl">
      <TabsList>
        <TabsTrigger value="settings">Status and settings</TabsTrigger>
        <TabsTrigger value="conf">nginx.conf</TabsTrigger>
      </TabsList>
      <TabsContent value="settings" className="flex flex-col gap-6">
        <ServiceStatus />
        <SettingsForm />
      </TabsContent>
      <TabsContent value="conf">
        <ConfigFilePanel source={{ kind: "nginx" }} />
      </TabsContent>
    </Tabs>
  )
}

/** A sentence and a badge for the container's state, from what Docker reports. */
export function describeContainer(c: NginxServiceStatus["container"]): {
  badge: string
  ok: boolean
  text: string
} {
  if (c.error) return { badge: "unknown", ok: false, text: c.error }
  if (!c.exists) {
    return {
      badge: "not created",
      ok: false,
      text: "The nginx container does not exist. It is created when the panel starts.",
    }
  }
  if (!c.running) {
    return { badge: c.status || "stopped", ok: false, text: "The nginx container is not running." }
  }
  const since = c.started_at ? ` since ${new Date(c.started_at).toLocaleString()}` : ""
  return { badge: "running", ok: true, text: `nginx is running${since}.` }
}

function ServiceStatus() {
  const { data, isPending, error } = useNginxService()

  return (
    <Card>
      <CardHeader>
        <CardTitle>Status</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        {isPending ? (
          <p className="text-muted-foreground">Asking Docker…</p>
        ) : error ? (
          <p role="alert" className="text-destructive">
            {error instanceof ApiError ? error.detail : "The status could not be loaded."}
          </p>
        ) : (
          <StatusBody status={data} />
        )}
      </CardContent>
    </Card>
  )
}

function StatusBody({ status }: { status: NginxServiceStatus }) {
  const c = describeContainer(status.container)
  const r = status.reconcile

  return (
    <>
      <div className="flex items-center gap-3">
        <Badge variant={c.ok ? "secondary" : "destructive"}>{c.badge}</Badge>
        <span>{c.text}</span>
      </div>

      {!r.done ? (
        <p className="text-muted-foreground">The panel is still setting nginx up after its start.</p>
      ) : r.ok ? null : (
        <div role="alert" className="border-destructive/40 bg-destructive/5 rounded-md border p-3">
          <p className="text-destructive font-medium">
            The panel could not bring nginx up when it started.
          </p>
          <ol className="mt-2 flex flex-col gap-1">
            {r.steps.map((step, i) => (
              <li key={`${step.name}-${i}`} className="flex gap-2 text-xs">
                <span className="w-10 shrink-0 font-medium">{step.ok ? "ok" : "failed"}</span>
                <span className="font-mono">{step.name}</span>
                <span className="text-muted-foreground">{step.detail}</span>
              </li>
            ))}
          </ol>
          {r.output && (
            <pre className="mt-2 overflow-x-auto font-mono text-xs whitespace-pre-wrap">
              {r.output.trimEnd()}
            </pre>
          )}
          <p className="text-muted-foreground mt-2 text-xs">
            Fix the cause, then restart the panel service to try again. The full error is in the
            panel's log.
          </p>
        </div>
      )}
    </>
  )
}

const timeouts: { key: keyof NginxSettings; label: string; hint: string }[] = [
  { key: "keepalive_timeout", label: "Keep-alive", hint: "How long an idle connection stays open." },
  {
    key: "client_header_timeout",
    label: "Request headers",
    hint: "How long a client may take to send its headers.",
  },
  {
    key: "client_body_timeout",
    label: "Request body",
    hint: "The longest pause allowed while a client uploads.",
  },
  { key: "send_timeout", label: "Response", hint: "The longest pause allowed while a client downloads." },
]

function SettingsForm() {
  const id = useId()
  const { data, isPending, error } = useNginxSettings()
  const save = useSaveNginxSettings()
  const [draft, setDraft] = useState<NginxSettings | null>(null)
  const [saved, setSaved] = useState(false)

  // The form follows the server: on first load and after every save.
  useEffect(() => {
    if (data) setDraft(data)
  }, [data])

  if (isPending || (!draft && !error)) {
    return <p className="text-muted-foreground text-sm">Loading settings…</p>
  }
  if (error || !draft || !data) {
    return (
      <p role="alert" className="text-destructive text-sm">
        {error instanceof ApiError ? error.detail : "The settings could not be loaded."}
      </p>
    )
  }

  const set = <K extends keyof NginxSettings>(key: K, value: NginxSettings[K]) => {
    setDraft({ ...draft, [key]: value })
    setSaved(false)
  }
  const dirty = JSON.stringify(draft) !== JSON.stringify(data)
  const badField = save.error instanceof ApiError ? save.error.field : null
  const fieldError = (key: keyof NginxSettings) =>
    badField === key && save.error instanceof ApiError ? (
      <p id={`${id}-${key}-error`} role="alert" className="text-destructive text-xs">
        {save.error.detail.replace(`${key}: `, "")}
      </p>
    ) : null

  return (
    <Card>
      <CardHeader>
        <CardTitle>Common settings</CardTitle>
      </CardHeader>
      <CardContent>
        <form
          className="flex flex-col gap-5"
          noValidate
          onSubmit={(e) => {
            e.preventDefault()
            save.mutate(draft, { onSuccess: () => setSaved(true) })
          }}
        >
          <div className="grid gap-5 sm:grid-cols-2">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-workers`}>Worker processes</Label>
              <Input
                id={`${id}-workers`}
                value={draft.worker_processes}
                onChange={(e) => set("worker_processes", e.target.value.trim())}
                aria-invalid={badField === "worker_processes"}
                aria-describedby={`${id}-workers-hint`}
              />
              <p id={`${id}-workers-hint`} className="text-muted-foreground text-xs">
                "auto" uses one per CPU core, or give a number.
              </p>
              {fieldError("worker_processes")}
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-body`}>Largest upload</Label>
              <Input
                id={`${id}-body`}
                value={draft.client_max_body_size}
                onChange={(e) => set("client_max_body_size", e.target.value.trim())}
                aria-invalid={badField === "client_max_body_size"}
                aria-describedby={`${id}-body-hint`}
              />
              <p id={`${id}-body-hint`} className="text-muted-foreground text-xs">
                A number with k, m or g, for example 100m. 0 removes the limit.
              </p>
              {fieldError("client_max_body_size")}
            </div>
          </div>

          <fieldset className="flex flex-col gap-3">
            <legend className="mb-3 text-sm font-medium">Timeouts, in seconds</legend>
            <div className="grid gap-5 sm:grid-cols-2">
              {timeouts.map(({ key, label, hint }) => (
                <div key={key} className="flex flex-col gap-1.5">
                  <Label htmlFor={`${id}-${key}`}>{label}</Label>
                  <Input
                    id={`${id}-${key}`}
                    type="number"
                    min={1}
                    max={3600}
                    value={String(draft[key])}
                    onChange={(e) => set(key, Number.parseInt(e.target.value, 10) || 0)}
                    aria-invalid={badField === key}
                    aria-describedby={`${id}-${key}-hint`}
                  />
                  <p id={`${id}-${key}-hint`} className="text-muted-foreground text-xs">
                    {hint}
                  </p>
                  {fieldError(key)}
                </div>
              ))}
            </div>
          </fieldset>

          <div className="flex flex-col gap-2">
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={draft.gzip}
                onChange={(e) => set("gzip", e.target.checked)}
              />
              Compress text responses (gzip)
            </label>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={draft.server_tokens}
                onChange={(e) => set("server_tokens", e.target.checked)}
              />
              Show the nginx version in responses and error pages
            </label>
          </div>

          {badField === null && (
            <ApplyError error={save.error} fallback="The settings could not be saved." />
          )}

          <div className="flex items-center gap-3">
            <Button type="submit" disabled={!dirty || save.isPending}>
              {save.isPending ? "Checking with nginx…" : "Save and reload"}
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={!dirty}
              onClick={() => {
                setDraft(data)
                save.reset()
              }}
            >
              Discard
            </Button>
            <span className="text-muted-foreground text-xs" aria-live="polite">
              {saved && !dirty ? "Saved. nginx reloaded." : ""}
            </span>
          </div>
        </form>
      </CardContent>
    </Card>
  )
}
