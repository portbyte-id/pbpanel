import { SkeletonRows } from "@/components/ui/skeleton"
import { useQueryClient } from "@tanstack/react-query"
import { useEffect, useId, useState } from "react"

import { ApplyError } from "@/components/ApplyError"
import { ConfigFilePanel } from "@/components/ConfigFilePanel"
import { BuildHistory, ExtensionChips, formatBuildTime } from "@/components/ImageBuilds"
import { JobsDrawer } from "@/components/JobsDrawer"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  ApiError,
  type ImageBuild,
  type JobRef,
  type NginxServiceStatus,
  type NginxSettings,
} from "@/lib/api"
import {
  queryKeys,
  useActivateNginxImageBuild,
  useNginxContainerAction,
  useNginxImage,
  useNginxService,
  useSetNginxVersion,
  useNginxSettings,
  useSaveNginxSettings,
  useUpdateNginxImage,
} from "@/lib/queries"

/** nginx as a whole: whether it is running, the common settings, and nginx.conf. */
export function NginxPage() {
  return (
    <Tabs defaultValue="settings" className="max-w-4xl">
      <TabsList>
        <TabsTrigger value="settings">Status</TabsTrigger>
        <TabsTrigger value="image">Image</TabsTrigger>
        <TabsTrigger value="common">Settings</TabsTrigger>
        <TabsTrigger value="conf">nginx.conf</TabsTrigger>
      </TabsList>
      <TabsContent value="settings">
        <ServiceStatus />
      </TabsContent>
      <TabsContent value="image">
        <ImageCard />
      </TabsContent>
      <TabsContent value="common">
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
  const client = useQueryClient()
  const { data, isPending, error } = useNginxService()
  const action = useNginxContainerAction()
  const [jobId, setJobId] = useState<string | null>(null)

  return (
    <Card>
      <CardHeader>
        <CardTitle>Status</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        {isPending ? (
          <SkeletonRows label="Asking Docker…" />
        ) : error ? (
          <p role="alert" className="text-destructive">
            {error instanceof ApiError ? error.detail : "The status could not be loaded."}
          </p>
        ) : (
          <>
            <StatusBody status={data} />
            <div className="flex flex-wrap items-center gap-3">
              <Button
                disabled={action.isPending || data.container.running}
                onClick={() => action.mutate("start", { onSuccess: (job: JobRef) => setJobId(job.id) })}
              >
                Start
              </Button>
              <Button
                variant="outline"
                disabled={action.isPending || !data.container.running}
                onClick={() => action.mutate("stop", { onSuccess: (job: JobRef) => setJobId(job.id) })}
              >
                Stop
              </Button>
              <Button
                variant="outline"
                disabled={action.isPending || !data.container.running}
                onClick={() => action.mutate("restart", { onSuccess: (job: JobRef) => setJobId(job.id) })}
              >
                Restart
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              Stopping nginx takes every site offline. A stop made here is kept when the panel
              restarts; use Start to bring nginx back.
            </p>
            <ApplyError error={action.error} fallback="The action could not be started." />
          </>
        )}
      </CardContent>
      <JobsDrawer
        jobId={jobId}
        onClose={() => {
          setJobId(null)
          void client.invalidateQueries({ queryKey: queryKeys.nginxService })
        }}
      />
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

/**
 * The nginx image: which build serves, the build it rolls back to, and the update
 * that builds a new one from the newest base. A build's job is watched in the drawer.
 */
export function ImageCard() {
  const client = useQueryClient()
  const { data, isPending, error } = useNginxImage()
  const update = useUpdateNginxImage()
  const activate = useActivateNginxImageBuild()
  const setVersion = useSetNginxVersion()
  const [jobId, setJobId] = useState<string | null>(null)
  const [pick, setPick] = useState<string | null>(null)

  return (
    <Card>
      <CardHeader>
        <CardTitle>Image</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4 text-sm">
        {isPending ? (
          <SkeletonRows label="Loading the image…" />
        ) : error ? (
          <p role="alert" className="text-destructive">
            {error instanceof ApiError ? error.detail : "The image could not be loaded."}
          </p>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-3">
              <Badge variant={data.present ? "secondary" : "destructive"}>
                {data.present ? "present" : "missing"}
              </Badge>
              <span>
                {data.present
                  ? "The nginx image is on this host."
                  : "The nginx image is not on this host."}
              </span>
            </div>

            <div className="flex flex-col gap-1.5">
              <Label htmlFor="nginx-version">nginx version</Label>
              <div className="flex flex-wrap items-center gap-3">
                <select
                  id="nginx-version"
                  value={pick ?? data.version}
                  onChange={(e) => setPick(e.target.value)}
                  className="border-input bg-background h-9 rounded-md border px-2 text-sm"
                >
                  {data.versions.map((v) => (
                    <option key={v} value={v}>
                      {v.startsWith("pqc-")
                        ? `${v.slice(4)} post-quantum (OpenSSL 3.5, Alpine, built on this host)`
                        : `${v} official`}
                      {v === data.version ? " (running)" : ""}
                    </option>
                  ))}
                </select>
                <Button
                  variant="outline"
                  disabled={
                    data.busy || setVersion.isPending || pick === null || pick === data.version
                  }
                  onClick={() =>
                    setVersion.mutate(pick ?? "", {
                      onSuccess: (job: JobRef) => {
                        setJobId(job.id)
                        setPick(null)
                      },
                    })
                  }
                >
                  {setVersion.isPending ? "Starting…" : "Switch version"}
                </Button>
              </div>
              <p className="text-muted-foreground text-xs">
                The new version is built, your current configuration is checked against it with
                nginx -t, and only then is nginx swapped over. If it fails, the running version
                stays.
              </p>
              <ApplyError error={setVersion.error} fallback="The version could not be changed." />
            </div>

            <dl className="grid gap-x-4 gap-y-2 sm:grid-cols-[max-content_1fr]">
              <dt className="text-muted-foreground">Base</dt>
              <dd className="font-mono text-xs">{data.base}</dd>
              <dt className="text-muted-foreground">Repository</dt>
              <dd className="font-mono text-xs">{data.repo}</dd>
              <dt className="text-muted-foreground">Serving</dt>
              <dd>
                {data.current ? (
                  <BuildSummary build={data.current} />
                ) : (
                  <span className="text-muted-foreground">No build yet.</span>
                )}
              </dd>
              {data.previous && (
                <>
                  <dt className="text-muted-foreground">Previous</dt>
                  <dd>
                    <BuildSummary build={data.previous} />
                  </dd>
                </>
              )}
            </dl>

            <div className="flex flex-wrap items-center gap-3">
              <Button
                disabled={data.busy || update.isPending}
                onClick={() => update.mutate(undefined, { onSuccess: (job: JobRef) => setJobId(job.id) })}
              >
                {update.isPending ? "Starting…" : "Update image"}
              </Button>
              {data.previous && data.previous.state === "ready" && (
                <Button
                  variant="outline"
                  disabled={data.busy || activate.isPending}
                  onClick={() =>
                    activate.mutate(data.previous?.id ?? "", {
                      onSuccess: (job: JobRef) => setJobId(job.id),
                    })
                  }
                >
                  Roll back
                </Button>
              )}
              {data.busy && (
                <span className="text-muted-foreground text-xs" aria-live="polite">
                  An image build is running.
                </span>
              )}
            </div>
            <ApplyError error={update.error ?? activate.error} fallback="The image could not be changed." />

            <div className="flex flex-col gap-2">
              <h3 className="text-sm font-medium">History</h3>
              <BuildHistory
                builds={data.builds}
                emptyText="No image builds yet."
                onActivate={(build) => activate.mutate(build.id, { onSuccess: (job: JobRef) => setJobId(job.id) })}
                activating={activate.isPending || data.busy}
                error={null}
                fallback=""
              />
            </div>
          </>
        )}
      </CardContent>

      <JobsDrawer
        jobId={jobId}
        onClose={() => {
          setJobId(null)
          void client.invalidateQueries({ queryKey: queryKeys.nginxImage, exact: true })
        }}
      />
    </Card>
  )
}

function BuildSummary({ build }: { build: ImageBuild }) {
  return (
    <span className="flex flex-wrap items-center gap-2">
      <span className="font-mono text-xs">{build.id}</span>
      <span className="text-muted-foreground text-xs">created {formatBuildTime(build.created_at)}</span>
      <ExtensionChips names={build.extensions} empty="extensions none" />
    </span>
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
    return <SkeletonRows label="Loading settings…" />
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
