import { useId, useState } from "react"

import { ApplyError, ApplyWarnings } from "@/components/ApplyError"
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
  type AppInput,
  type AppRestart,
  type AppType,
  type NodeVersion,
  type PanelApp,
} from "@/lib/api"
import { useRuntimes, useSaveApp } from "@/lib/queries"

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

/** The major versions installed, newest first: an app asks for "22", not "22.11.0". */
export function nodeMajors(installed: NodeVersion[], current = ""): string[] {
  const majors = new Set(installed.map((n) => n.version.split(".")[0] ?? ""))
  if (current !== "") majors.add(current)
  return [...majors].filter((m) => m !== "").sort((a, b) => Number(b) - Number(a))
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
  onSubmit: (input: AppInput) => void
  onCancel: () => void
  pending: boolean
  error: unknown
}) {
  const id = useId()
  const [name, setName] = useState(app?.name ?? "")
  const [type, setType] = useState<AppType>(app?.type ?? "go")
  const [command, setCommand] = useState(app?.command ?? "")
  const [args, setArgs] = useState((app?.args ?? []).join("\n"))
  const [workdir, setWorkdir] = useState(app?.workdir ?? "")
  const [env, setEnv] = useState(formatEnv(app?.env ?? {}))
  const [user, setUser] = useState(app?.user ?? "www")
  const majors = nodeMajors(nodeVersions, app?.node_version ?? "")
  const [nodeVersion, setNodeVersion] = useState(app?.node_version ?? majors[0] ?? "")
  const [port, setPort] = useState(app?.port == null ? "" : String(app.port))
  const [restart, setRestart] = useState<AppRestart>(app?.restart ?? "always")
  const [autostart, setAutostart] = useState(app?.autostart ?? true)
  const [localError, setLocalError] = useState<{ field: string; text: string } | null>(null)

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
    if (type === "node") input.node_version = nodeVersion
    onSubmit(input)
  }

  const noNode = type === "node" && majors.length === 0

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
        <Input
          id={`${id}-command`}
          value={command}
          onChange={(e) => setCommand(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          placeholder={type === "node" ? "server.js" : "/www/services/apps/my-app/bin/my-app"}
          required
          aria-invalid={invalid("command")}
          aria-describedby={describedBy("command")}
        />
        {fieldError("command")}
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
        <Input
          id={`${id}-workdir`}
          value={workdir}
          onChange={(e) => setWorkdir(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          placeholder={`/www/services/apps/${name.trim() || "<name>"}`}
          aria-invalid={invalid("workdir")}
          aria-describedby={describedBy("workdir")}
        />
        {fieldError("workdir")}
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-env`}>Environment</Label>
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
            Passed to the app as PORT. It also lets you create a proxy site for it.
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
  const runtimes = useRuntimes()
  // A save can succeed with a warning (the app would not stay up). It is read
  // before the dialog closes, so it is shown in place of the form.
  const [warnings, setWarnings] = useState<string[] | null>(null)

  const close = (next: boolean) => {
    if (!next) {
      save.reset()
      setWarnings(null)
    }
    onOpenChange(next)
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
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
            pending={save.isPending}
            error={save.error}
            onCancel={() => close(false)}
            onSubmit={(input) =>
              save.mutate(
                { name: app?.name, app: input },
                {
                  onSuccess: (res) => {
                    if (res.warnings.length > 0) setWarnings(res.warnings)
                    else close(false)
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
