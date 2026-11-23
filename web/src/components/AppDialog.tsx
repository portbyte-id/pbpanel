import { useId, useState } from "react"

import { ApplyError, ApplyWarnings } from "@/components/ApplyError"
import { APPS_ROOT, PathPicker, isEnvFile, parentOf } from "@/components/PathPicker"
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
import { Select } from "@/components/ui/select"
import {
  ApiError,
  api,
  type AppInput,
  type AppRestart,
  type AppType,
  type NodeVersion,
  type PanelApp,
} from "@/lib/api"
import { useCreateSite, useRuntimes, useSaveApp } from "@/lib/queries"
import { parseAliases } from "@/components/CreateSiteDialog"

/** The domain an app is bound to when it is created. */
export interface AppDomain {
  domain: string
  aliases: string[]
}

/** One argument per line. Inner spaces are kept: an argument may contain them. */
export function parseLines(text: string): string[] {
  return text
    .split("\n")
    .map((l) => l.replace(/\r$/, ""))
    .filter((l) => l.trim() !== "")
}

/** `KEY=value`, one per line. Blank lines and `#` comments are skipped. */
export function parseEnv(text: string): { env: Record<string, string>; error: string | null } {
  const env: Record<string, string> = {}
  for (const raw of text.split("\n")) {
    const line = raw.replace(/\r$/, "")
    if (line.trim() === "" || line.trimStart().startsWith("#")) continue
    const eq = line.indexOf("=")
    if (eq <= 0) return { env, error: `"${line.trim()}" is not KEY=value.` }
    env[line.slice(0, eq).trim()] = line.slice(eq + 1)
  }
  return { env, error: null }
}

export function formatEnv(env: Record<string, string>): string {
  return Object.keys(env)
    .sort()
    .map((k) => `${k}=${env[k]}`)
    .join("\n")
}

/**
 * The Node.js versions an app can pick, newest first. Installed versions are
 * offered by major ("22", not "22.11.0"). An app's own stored value is kept as it
 * is, even when it is a full version, so editing never silently changes what it runs.
 */
export function nodeMajors(installed: NodeVersion[], current = ""): string[] {
  const versions = new Set(installed.map((n) => n.version.split(".")[0] ?? ""))
  if (current.trim() !== "") versions.add(current.trim())
  const parts = (v: string) => v.split(".").map((p) => Number.parseInt(p, 10) || 0)
  return [...versions]
    .filter((v) => v !== "")
    .sort((a, b) => {
      const pa = parts(a)
      const pb = parts(b)
      for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
        const d = (pb[i] ?? 0) - (pa[i] ?? 0)
        if (d !== 0) return d
      }
      return 0
    })
}

const textarea =
  "border-input bg-transparent shadow-xs focus-visible:border-ring focus-visible:ring-ring/50 " +
  "aria-invalid:border-destructive min-h-20 w-full rounded-md border px-3 py-2 font-mono text-sm " +
  "outline-none focus-visible:ring-[3px]"

/**
 * The app form, without the dialog or the request around it. A rejected field
 * (`invalid_input` or `conflict` with `field`) is shown under that field; anything
 * else, including supervisord refusing the generated file, once under the form.
 */
export function AppForm({
  app,
  nodeVersions,
  onSubmit,
  onCancel,
  pending,
  error,
}: {
  /** Set when editing: the name is then fixed. */
  app: PanelApp | null
  nodeVersions: NodeVersion[]
  /** `bind` is set only when the operator asked for a domain: the app is then put behind a proxy site. */
  onSubmit: (input: AppInput, bind?: AppDomain) => void
  onCancel: () => void
  pending: boolean
  error: unknown
}) {
  const id = useId()
  const [domain, setDomain] = useState("")
  const [aliases, setAliases] = useState("")
  const [name, setName] = useState(app?.name ?? "")
  const [type, setType] = useState<AppType>(app?.type ?? "go")
  const [command, setCommand] = useState(app?.command ?? "")
  const [args, setArgs] = useState((app?.args ?? []).join("\n"))
  const [workdir, setWorkdir] = useState(app?.workdir ?? "")
  const [env, setEnv] = useState(formatEnv(app?.env ?? {}))
  const [user, setUser] = useState(app?.user ?? "www")
  const majors = nodeMajors(nodeVersions, app?.node_version ?? "")
  // An app saved without a version (e.g. a Go app switched to Node.js) starts on the
  // first offered one, so the select never shows a value the form would not send.
  const [nodeVersion, setNodeVersion] = useState(app?.node_version || (majors[0] ?? ""))
  const [port, setPort] = useState(app?.port == null ? "" : String(app.port))
  const [restart, setRestart] = useState<AppRestart>(app?.restart ?? "always")
  const [autostart, setAutostart] = useState(app?.autostart ?? true)
  const [localError, setLocalError] = useState<{ field: string; text: string } | null>(null)
  // Which path field's browser is open, and the file the last env load came from.
  const [picking, setPicking] = useState<"command" | "workdir" | "env" | null>(null)
  const [envFrom, setEnvFrom] = useState<string | null>(null)
  const [envLoading, setEnvLoading] = useState(false)

  const loadEnvFile = async (path: string) => {
    setEnvLoading(true)
    try {
      const file = await api.fmOpenFile(path)
      const parsed = parseEnv(file.content)
      if (parsed.error) {
        setLocalError({ field: "env", text: `${path}: ${parsed.error}` })
        return
      }
      // Merged, not replaced: keys the operator typed and the file lacks stay.
      setEnv((current) => formatEnv({ ...parseEnv(current).env, ...parsed.env }))
      setEnvFrom(path)
      setLocalError(null)
    } catch (err) {
      setLocalError({
        field: "env",
        text: err instanceof ApiError ? `${path}: ${err.detail}` : `The file ${path} could not be read.`,
      })
    } finally {
      setEnvLoading(false)
    }
  }

  const apiField =
    error instanceof ApiError && (error.code === "invalid_input" || error.code === "conflict")
      ? (error.field ?? null)
      : null
  // args[2] is reported against the one "args" box.
  const fieldName = apiField?.replace(/\[\d+\]$/, "") ?? null
  const fieldError = (n: string) => {
    if (localError?.field === n) {
      return (
        <p id={`${id}-${n}-error`} role="alert" className="text-destructive text-xs">
          {localError.text}
        </p>
      )
    }
    return fieldName === n && error instanceof ApiError ? (
      <p id={`${id}-${n}-error`} role="alert" className="text-destructive text-xs">
        {error.detail.replace(`${apiField}: `, "")}
      </p>
    ) : null
  }
  const invalid = (n: string) => fieldName === n || localError?.field === n
  const describedBy = (n: string) => (invalid(n) ? `${id}-${n}-error` : undefined)
  // A field error is shown once, under its field; the rest go under the form.
  const generalError = fieldName === null ? error : null

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    const parsed = parseEnv(env)
    if (parsed.error) {
      setLocalError({ field: "env", text: parsed.error })
      return
    }
    let portNumber: number | null = null
    if (port.trim() !== "") {
      portNumber = Number(port)
      if (!Number.isInteger(portNumber) || portNumber < 1 || portNumber > 65535) {
        setLocalError({ field: "port", text: "Enter a port between 1 and 65535, or leave it empty." })
        return
      }
    }
    if (domain.trim() !== "" && portNumber === null) {
      setLocalError({ field: "domain", text: "Set a port first: the domain passes requests to it." })
      return
    }
    setLocalError(null)
    const input: AppInput = {
      name: name.trim(),
      type,
      command: command.trim(),
      args: parseLines(args),
      env: parsed.env,
      restart,
      autostart,
      port: portNumber,
    }
    if (workdir.trim() !== "") input.workdir = workdir.trim()
    if (user.trim() !== "") input.user = user.trim()
    if (type === "node") {
      if (nodeVersion.trim() === "") {
        setLocalError({ field: "node_version", text: "Choose the Node.js version this app runs on." })
        return
      }
      input.node_version = nodeVersion.trim()
    }
    if (app === null && domain.trim() !== "") {
      onSubmit(input, { domain: domain.trim(), aliases: parseAliases(aliases) })
    } else {
      onSubmit(input)
    }
  }

  const noNode = type === "node" && majors.length === 0
  // Where each browser opens: the folder a path already names, else the app's own.
  const appDir = name.trim() === "" ? APPS_ROOT : `${APPS_ROOT}/${name.trim()}`
  const workdirStart = workdir.trim().startsWith("/") ? workdir.trim() : appDir
  const commandStart = command.trim().startsWith("/")
    ? parentOf(command.trim())
    : workdir.trim().startsWith("/")
      ? workdir.trim()
      : appDir

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-name`}>Name</Label>
        <Input
          id={`${id}-name`}
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={app !== null}
          autoComplete="off"
          spellCheck={false}
          placeholder="my-app"
          required
          aria-invalid={invalid("name")}
          aria-describedby={describedBy("name")}
        />
        {fieldError("name")}
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-type`}>Kind</Label>
        <Select
          id={`${id}-type`}
          value={type}
          onChange={(e) => setType(e.target.value as AppType)}
          aria-invalid={invalid("type")}
        >
          <option value="go">A compiled program (Go or any binary)</option>
          <option value="node">A Node.js app</option>
          <option value="command">Another command</option>
        </Select>
        {fieldError("type")}
      </div>

      {type === "node" && (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-node`}>Node.js version</Label>
          <Select
            id={`${id}-node`}
            value={nodeVersion}
            onChange={(e) => setNodeVersion(e.target.value)}
            disabled={noNode}
            aria-invalid={invalid("node_version")}
            aria-describedby={describedBy("node_version")}
          >
            {majors.map((m) => (
              <option key={m} value={m}>
                Node.js {m}
              </option>
            ))}
          </Select>
          {noNode && (
            <p className="text-muted-foreground text-xs">
              No version is installed yet. Install one on the Runtimes page first.
            </p>
          )}
          {fieldError("node_version")}
        </div>
      )}

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-command`}>{type === "node" ? "Entry script" : "Program path"}</Label>
        <div className="flex gap-2">
          <Input
            id={`${id}-command`}
            value={command}
            onChange={(e) => setCommand(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            placeholder={type === "node" ? "server.js" : "/www/services/sites/my-app/bin/my-app"}
            required
            aria-invalid={invalid("command")}
            aria-describedby={describedBy("command")}
          />
          <Button
            type="button"
            variant="outline"
            aria-label="Browse… for the program"
            onClick={() => setPicking("command")}
          >
            Browse…
          </Button>
        </div>
        {fieldError("command")}
        <PathPicker
          open={picking === "command"}
          onOpenChange={(open) => !open && setPicking(null)}
          start={commandStart}
          mode="file"
          title={type === "node" ? "Choose the entry script" : "Choose the program"}
          description="Open a folder, then choose a file in it. The whole filesystem is browsable."
          onPick={setCommand}
        />
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-args`}>Arguments</Label>
        <textarea
          id={`${id}-args`}
          className={textarea}
          value={args}
          onChange={(e) => setArgs(e.target.value)}
          spellCheck={false}
          placeholder="One per line"
          aria-invalid={invalid("args")}
          aria-describedby={describedBy("args")}
        />
        {fieldError("args")}
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-workdir`}>Working directory</Label>
        <div className="flex gap-2">
          <Input
            id={`${id}-workdir`}
            value={workdir}
            onChange={(e) => setWorkdir(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            placeholder={`/www/services/sites/${name.trim() || "<name>"}`}
            aria-invalid={invalid("workdir")}
            aria-describedby={describedBy("workdir")}
          />
          <Button
            type="button"
            variant="outline"
            aria-label="Browse… for the working directory"
            onClick={() => setPicking("workdir")}
          >
            Browse…
          </Button>
        </div>
        {fieldError("workdir")}
        <PathPicker
          open={picking === "workdir"}
          onOpenChange={(open) => !open && setPicking(null)}
          start={workdirStart}
          mode="dir"
          title="Choose the working directory"
          description="The folder the app runs in. Open a folder, then choose it; the whole filesystem is browsable."
          onPick={setWorkdir}
        />
      </div>

      <div className="flex flex-col gap-1.5">
        <div className="flex items-center justify-between gap-2">
          <Label htmlFor={`${id}-env`}>Environment</Label>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={envLoading}
            onClick={() => setPicking("env")}
          >
            {envLoading ? "Reading…" : "Load .env file…"}
          </Button>
        </div>
        <textarea
          id={`${id}-env`}
          className={textarea}
          value={env}
          onChange={(e) => setEnv(e.target.value)}
          spellCheck={false}
          placeholder="KEY=value, one per line"
          aria-invalid={invalid("env")}
          aria-describedby={describedBy("env")}
        />
        {fieldError("env")}
        {envFrom !== null && (
          <p className="text-muted-foreground text-xs">
            Read from {envFrom}. Keys typed here that the file did not set were kept.
          </p>
        )}
        <PathPicker
          open={picking === "env"}
          onOpenChange={(open) => !open && setPicking(null)}
          start={workdirStart}
          mode="file"
          filter={isEnvFile}
          title="Choose a .env file"
          description="Its KEY=value lines are added to the box above, replacing only the keys it sets. Only .env files are listed."
          onPick={(p) => void loadEnvFile(p)}
        />
      </div>

      <div className="grid grid-cols-2 gap-4">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-port`}>Port</Label>
          <Input
            id={`${id}-port`}
            value={port}
            onChange={(e) => setPort(e.target.value)}
            inputMode="numeric"
            autoComplete="off"
            placeholder="optional"
            aria-invalid={invalid("port")}
            aria-describedby={describedBy("port")}
          />
          {fieldError("port")}
          <p className="text-muted-foreground text-xs">
            Passed to the app as PORT. A domain needs it.
          </p>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-user`}>Runs as</Label>
          <Input
            id={`${id}-user`}
            value={user}
            onChange={(e) => setUser(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            aria-invalid={invalid("user")}
            aria-describedby={describedBy("user")}
          />
          {fieldError("user")}
        </div>
      </div>

      {app === null && (
        <fieldset className="border-input flex flex-col gap-3 rounded-md border p-3">
          <legend className="px-1 text-sm font-medium">Domain</legend>
          <p className="text-muted-foreground text-xs">
            Optional. nginx answers for the domain and passes requests to the app's port. You can
            add or change domains later from the Sites page.
          </p>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-domain`}>Domain</Label>
            <Input
              id={`${id}-domain`}
              value={domain}
              onChange={(e) => setDomain(e.target.value)}
              placeholder="app.example.com"
              autoComplete="off"
              spellCheck={false}
              aria-invalid={invalid("domain")}
              aria-describedby={describedBy("domain")}
            />
            {fieldError("domain")}
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-aliases`}>Other domains</Label>
            <Input
              id={`${id}-aliases`}
              value={aliases}
              onChange={(e) => setAliases(e.target.value)}
              placeholder="www.app.example.com"
              autoComplete="off"
              spellCheck={false}
              disabled={domain.trim() === ""}
            />
          </div>
        </fieldset>
      )}

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-restart`}>When it exits</Label>
        <Select
          id={`${id}-restart`}
          value={restart}
          onChange={(e) => setRestart(e.target.value as AppRestart)}
          aria-invalid={invalid("restart")}
        >
          <option value="always">Start it again</option>
          <option value="on-failure">Start it again only if it failed</option>
          <option value="never">Leave it stopped</option>
        </Select>
      </div>

      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={autostart} onChange={(e) => setAutostart(e.target.checked)} />
        Start it when supervisord starts
      </label>

      <ApplyError error={generalError} fallback="The app could not be saved." />

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={pending || noNode}>
          {pending ? "Saving…" : app ? "Save changes" : "Create app"}
        </Button>
      </DialogFooter>
    </form>
  )
}

/** The add and edit dialog. Closing it forgets a failed save. */
export function AppDialog({
  open,
  onOpenChange,
  app,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  app: PanelApp | null
}) {
  const save = useSaveApp()
  const createSite = useCreateSite()
  const runtimes = useRuntimes()
  // Set when the app was saved but its domain could not be bound.
  const [siteError, setSiteError] = useState<string | null>(null)
  // A save can succeed with a warning (the app would not stay up). It is read
  // before the dialog closes, so it is shown in place of the form.
  const [warnings, setWarnings] = useState<string[] | null>(null)

  const close = (next: boolean) => {
    if (!next) {
      save.reset()
      createSite.reset()
      setWarnings(null)
      setSiteError(null)
    }
    onOpenChange(next)
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-[720px]">
        <DialogHeader>
          <DialogTitle>{app ? `Edit ${app.name}` : "Add an app"}</DialogTitle>
          <DialogDescription>
            supervisord checks the program file before anything changes. If it refuses it, the
            previous definition stays.
          </DialogDescription>
        </DialogHeader>
        {open && warnings !== null && (
          <div className="flex flex-col gap-3">
            <p className="text-sm">The app was saved, with a problem:</p>
            <ApplyWarnings warnings={warnings} />
            {siteError !== null && (
              <p role="alert" className="text-destructive text-sm">
                The domain was not added: {siteError}
              </p>
            )}
            <DialogFooter>
              <Button onClick={() => close(false)}>Done</Button>
            </DialogFooter>
          </div>
        )}
        {open && warnings === null && (
          <AppForm
            // Remounted when the runtimes arrive, so the Node picker starts on one.
            key={(runtimes.data?.node ?? []).map((n) => n.version).join(",")}
            app={app}
            nodeVersions={runtimes.data?.node ?? []}
            pending={save.isPending || createSite.isPending}
            error={save.error}
            onCancel={() => close(false)}
            onSubmit={(input, bind) =>
              save.mutate(
                { name: app?.name, app: input },
                {
                  onSuccess: (res) => {
                    const finish = (extra: string | null) => {
                      if (extra !== null) setSiteError(extra)
                      if (res.warnings.length > 0 || extra !== null) {
                        setWarnings(extra !== null && res.warnings.length === 0 ? [] : res.warnings)
                      } else close(false)
                    }
                    if (!bind) return finish(null)
                    createSite.mutate(
                      { domain: bind.domain, aliases: bind.aliases, kind: "proxy", proxy_target: res.app.proxy_url },
                      {
                        onSuccess: () => finish(null),
                        onError: (e) =>
                          finish(e instanceof ApiError ? e.detail : "The site could not be created."),
                      },
                    )
                  },
                },
              )
            }
          />
        )}
      </DialogContent>
    </Dialog>
  )
}
