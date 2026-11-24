import { Fragment, useId, useState } from "react"

import { ApplyError, ApplyWarnings } from "@/components/ApplyError"
import { APPS_ROOT, PathPicker, isEnvFile, parentOf } from "@/components/PathPicker"
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
import { cn } from "@/lib/utils"
import { parseAliases } from "@/components/CreateSiteDialog"

/** The domain an app is bound to when it is created. */
export interface AppDomain {
  domain: string
  aliases: string[]
}

/**
 * The part of a new app's form on show. Settings is the program (its kind and
 * how it runs); Domain is the site in front of it. The dialog's sidebar picks it.
 */
export type AppFormPart = "settings" | "domain"

/** The fields that belong to the Domain part. Everything else is Settings. */
const domainFields = ["domain", "aliases"]

/** One argument per line. Inner spaces are kept: an argument may contain them. */
export function parseLines(text: string): string[] {
  return text
    .split("\n")
    .map((l) => l.replace(/\r$/, ""))
    .filter((l) => l.trim() !== "")
}

/**
 * Splits an execution command into the program and its arguments, by shell-like
 * rules: whitespace separates, single quotes are literal, double quotes allow
 * `\"` and `\\`, and a backslash outside quotes escapes the next character.
 * Nothing is expanded. `error` is set for an unterminated quote.
 */
export function splitCommandLine(text: string): { argv: string[]; error: string | null } {
  const argv: string[] = []
  let cur = ""
  let has = false
  let quote: "'" | '"' | null = null
  for (let i = 0; i < text.length; i++) {
    const c = text[i] as string
    if (quote === "'") {
      if (c === "'") quote = null
      else cur += c
    } else if (quote === '"') {
      if (c === '"') quote = null
      else if (c === "\\" && (text[i + 1] === '"' || text[i + 1] === "\\")) cur += text[++i]
      else cur += c
    } else if (c === "'" || c === '"') {
      quote = c
      has = true
    } else if (c === "\\" && i + 1 < text.length && text[i + 1] !== "\n") {
      cur += text[++i]
      has = true
    } else if (/\s/.test(c)) {
      if (has || cur !== "") argv.push(cur)
      cur = ""
      has = false
    } else {
      cur += c
      has = true
    }
  }
  if (quote !== null) return { argv, error: "A quote is not closed." }
  if (has || cur !== "") argv.push(cur)
  return { argv, error: null }
}

/** The inverse of {@link splitCommandLine}: quotes only the words that need it. */
export function joinCommandLine(argv: string[]): string {
  return argv
    .map((a) => {
      if (a !== "" && /^[A-Za-z0-9_@+=:,./-]+$/.test(a)) return a
      if (!a.includes("'")) return `'${a}'`
      return `"${a.replace(/(["\\])/g, "\\$1")}"`
    })
    .join(" ")
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
 * The app form, without the dialog or the request around it. The kind and the
 * part on show are owned by the dialog (its sidebar picks them), so the form only
 * draws them. A rejected field (`invalid_input` or `conflict` with `field`) is
 * shown under that field; anything else, including supervisord refusing the
 * generated file, once under the form.
 */
export function AppForm({
  app,
  type,
  onTypeChange,
  part,
  onPartChange,
  nodeVersions,
  onSubmit,
  onCancel,
  pending,
  error,
}: {
  /** Set when editing: the name is then fixed. */
  app: PanelApp | null
  /** The kind: a new app's comes from the sidebar; an existing app's from the Kind box. */
  type: AppType
  onTypeChange: (type: AppType) => void
  /** A new app shows one part at a time. An existing app always shows Settings. */
  part: AppFormPart
  onPartChange: (part: AppFormPart) => void
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
  const [shownType, setShownType] = useState(type)
  const [command, setCommand] = useState(app?.command ?? "")
  const [args, setArgs] = useState((app?.args ?? []).join("\n"))
  // Go and binary apps show program and arguments as one execution command line.
  const [line, setLine] = useState(joinCommandLine([...(app?.command ? [app.command] : []), ...(app?.args ?? [])]))
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

  // The kind changed from outside (the sidebar or the Kind box): carry the text over.
  // Done while rendering, the way React expects a state to follow a prop.
  if (type !== shownType) {
    setShownType(type)
    if (shownType === "node") {
      setLine(joinCommandLine([...(command.trim() ? [command.trim()] : []), ...parseLines(args)]))
    } else if (type === "node") {
      const { argv, error: splitError } = splitCommandLine(line)
      if (!splitError) {
        setCommand(argv[0] ?? "")
        setArgs(argv.slice(1).join("\n"))
      }
    }
  }

  const loadEnvFile = async (path: string) => {
    setEnvLoading(true)
    try {
      const file = await api.fmOpenFile(path)
      const parsed = parseEnv(file.content)
      if (parsed.error) {
        onPartChange("settings")
        setLocalError({ field: "env", text: `${path}: ${parsed.error}` })
        return
      }
      // Merged, not replaced: keys the operator typed and the file lacks stay.
      setEnv((current) => formatEnv({ ...parseEnv(current).env, ...parsed.env }))
      setEnvFrom(path)
      setLocalError(null)
    } catch (err) {
      onPartChange("settings")
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
  // With one command line there is no args box, so an argument error shows under it.
  const rawField = apiField?.replace(/\[\d+\]$/, "") ?? null
  const fieldName = rawField === "args" && type !== "node" ? "command" : rawField
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

  // A settings field's error is shown on Settings, so the form moves there first.
  const failSettings = (field: string, text: string) => {
    onPartChange("settings")
    setLocalError({ field, text })
  }

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    const parsed = parseEnv(env)
    if (parsed.error) {
      failSettings("env", parsed.error)
      return
    }
    let portNumber: number | null = null
    if (port.trim() !== "") {
      portNumber = Number(port)
      if (!Number.isInteger(portNumber) || portNumber < 1 || portNumber > 65535) {
        failSettings("port", "Enter a port between 1 and 65535, or leave it empty.")
        return
      }
    }
    if (domain.trim() !== "" && portNumber === null) {
      onPartChange("domain")
      setLocalError({ field: "domain", text: "Set a port first: the domain passes requests to it." })
      return
    }
    let programPath = command.trim()
    let programArgs = parseLines(args)
    if (type !== "node") {
      const split = splitCommandLine(line)
      if (split.error) {
        failSettings("command", split.error)
        return
      }
      programPath = split.argv[0] ?? ""
      programArgs = split.argv.slice(1)
    }
    setLocalError(null)
    const input: AppInput = {
      name: name.trim(),
      type,
      command: programPath,
      args: programArgs,
      env: parsed.env,
      restart,
      autostart,
      port: portNumber,
    }
    if (workdir.trim() !== "") input.workdir = workdir.trim()
    if (user.trim() !== "") input.user = user.trim()
    if (type === "node") {
      if (nodeVersion.trim() === "") {
        failSettings("node_version", "Choose the Node.js version this app runs on.")
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

  const pickProgram = (path: string) => {
    if (type === "node") {
      setCommand(path)
      return
    }
    // Only the program changes; the arguments after it stay.
    const { argv, error: splitError } = splitCommandLine(line)
    setLine(joinCommandLine([path, ...(splitError ? [] : argv.slice(1))]))
  }

  const noNode = type === "node" && majors.length === 0
  // Where each browser opens: the folder a path already names, else the app's own.
  const appDir = name.trim() === "" ? APPS_ROOT : `${APPS_ROOT}/${name.trim()}`
  const workdirStart = workdir.trim().startsWith("/") ? workdir.trim() : appDir
  const programNow = type === "node" ? command.trim() : (splitCommandLine(line).argv[0] ?? "")
  const commandStart = programNow.startsWith("/")
    ? parentOf(programNow)
    : workdir.trim().startsWith("/")
      ? workdir.trim()
      : appDir
  const onSettings = app !== null || part === "settings"

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      {onSettings && (
        <>
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

          {app !== null && (
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-type`}>Kind</Label>
              <Select
                id={`${id}-type`}
                value={type}
                onChange={(e) => onTypeChange(e.target.value as AppType)}
                aria-invalid={invalid("type")}
              >
                <option value="go">A compiled program (Go or any binary)</option>
                <option value="node">A Node.js app</option>
                <option value="command">Another command</option>
              </Select>
              {fieldError("type")}
            </div>
          )}

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
            <Label htmlFor={`${id}-command`}>{type === "node" ? "Entry script" : "Execution command"}</Label>
            <div className="flex gap-2">
              <Input
                id={`${id}-command`}
                value={type === "node" ? command : line}
                onChange={(e) => (type === "node" ? setCommand(e.target.value) : setLine(e.target.value))}
                autoComplete="off"
                spellCheck={false}
                placeholder={
                  type === "node" ? "server.js" : "/www/services/sites/my-app/bin/my-app --listen :3000"
                }
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
            {type !== "node" && (
              <p className="text-muted-foreground text-xs">
                The program's absolute path, then its arguments. Quote an argument that contains spaces.
              </p>
            )}
            {fieldError("command")}
            <PathPicker
              open={picking === "command"}
              onOpenChange={(open) => !open && setPicking(null)}
              start={commandStart}
              mode="file"
              title={type === "node" ? "Choose the entry script" : "Choose the program"}
              description="Open a folder, then choose a file in it. The whole filesystem is browsable."
              onPick={pickProgram}
            />
          </div>

          {type === "node" && (
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
          )}

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
        </>
      )}

      {!onSettings && (
        <fieldset className="border-input flex flex-col gap-3 rounded-md border p-3">
          <legend className="px-1 text-sm font-medium">Domain</legend>
          <p className="text-muted-foreground text-xs">
            Optional. nginx answers for the domain and passes requests to the app's port, which is set
            on Settings. You can add or change domains later from the Sites page.
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

      {onSettings && (
        <>
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
        </>
      )}

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

/**
 * The sections in the dialog's sidebar of an existing app. `site:<section>` is a
 * section of the menu of the app's domain (Domain manager, Reverse proxy, Config…).
 */
export type AppSection = "settings" | "logs" | "delete" | `site:${string}`

/** The domain menu of an app: a heading and the sections under it. */
export interface AppSiteNav {
  heading: string
  items: { id: AppSection; label: string }[]
}

/** The kinds a new app can be, in the order the sidebar lists them. */
const newAppKinds: { type: AppType; label: string }[] = [
  { type: "go", label: "Go or binary" },
  { type: "node", label: "Node.js app" },
  { type: "command", label: "Other command" },
]

interface NavItem {
  key: string
  /** A small heading drawn above this item, starting a group. */
  heading?: string
  label: string
  active: boolean
  onSelect: () => void
}

/**
 * The add and manage dialog: a sidebar on the left, the section on the right.
 * An existing app's sidebar lists its sections. A new app's sidebar lists its kind
 * (Go or binary, Node.js, other command) and its Domain; the form shows the kind's
 * program settings, or the domain. Settings is the form; the other sections come
 * from `renderSection`. Closing it forgets a failed save.
 */
export function AppDialog({
  open,
  onOpenChange,
  app,
  defaultType,
  section = "settings",
  onSectionChange,
  renderSection,
  siteNav,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  app: PanelApp | null
  defaultType?: AppType
  section?: AppSection
  onSectionChange?: (section: AppSection) => void
  renderSection?: (section: AppSection, app: PanelApp) => React.ReactNode
  /** The domain menu, listed between the app's own sections and Delete. */
  siteNav?: AppSiteNav
}) {
  const save = useSaveApp()
  const createSite = useCreateSite()
  const runtimes = useRuntimes()
  const startKind = app?.type ?? defaultType ?? "go"
  // The kind and the part of the form on show. A new app's sidebar sets both.
  const [kind, setKind] = useState<AppType>(startKind)
  const [part, setPart] = useState<AppFormPart>("settings")
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
      setKind(startKind)
      setPart("settings")
    }
    onOpenChange(next)
  }

  const appItem = (id: AppSection, label: string, heading?: string): NavItem => ({
    key: id,
    heading,
    label,
    active: section === id,
    onSelect: () => onSectionChange?.(id),
  })
  const nav: NavItem[] = app
    ? [
        appItem("settings", "Settings"),
        appItem("logs", "Logs & control"),
        ...(siteNav?.items ?? []).map((it, i) => appItem(it.id, it.label, i === 0 ? siteNav?.heading : undefined)),
        appItem("delete", "Delete app", siteNav ? "App" : undefined),
      ]
    : [
        ...newAppKinds.map((k) => ({
          key: k.type,
          label: k.label,
          active: part === "settings" && kind === k.type,
          onSelect: () => {
            setKind(k.type)
            setPart("settings")
          },
        })),
        {
          key: "domain",
          label: "Domain",
          active: part === "domain",
          onSelect: () => setPart("domain"),
        },
      ]

  const settings = (
    <>
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
            type={kind}
            onTypeChange={setKind}
            part={part}
            onPartChange={setPart}
            nodeVersions={runtimes.data?.node ?? []}
            pending={save.isPending || createSite.isPending}
            error={save.error}
            onCancel={() => close(false)}
            onSubmit={(input, bind) =>
              save.mutate(
                { name: app?.name, app: input },
                {
                  onError: (e) => {
                    // A rejected field that is not on the Domain part is shown on Settings.
                    if (e instanceof ApiError && e.field && !domainFields.includes(e.field)) setPart("settings")
                  },
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
    </>
  )

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="h-[min(760px,90dvh)] grid-rows-[auto_minmax(0,1fr)] gap-0 overflow-hidden p-0 sm:max-w-[880px]">
        <DialogHeader className="border-b px-4 py-3 pr-12">
          <DialogTitle className="flex flex-wrap items-center gap-2">
            <span>{app ? `Manage app [${app.name}]` : "Add an app"}</span>
            {app && <Badge variant="secondary">{app.type === "node" ? `Node.js ${app.node_version}` : app.type === "go" ? "Program" : "Command"}</Badge>}
          </DialogTitle>
          <DialogDescription>
            supervisord checks the program file before anything changes. If it refuses it, the
            previous definition stays.
          </DialogDescription>
        </DialogHeader>
        <div className="grid min-h-0 grid-cols-1 grid-rows-[auto_minmax(0,1fr)] sm:grid-cols-[11.5rem_minmax(0,1fr)] sm:grid-rows-1">
          <nav
            aria-label={app ? "App sections" : "New app"}
            className="bg-muted/30 flex gap-1 overflow-x-auto border-b p-2 sm:flex-col sm:overflow-y-auto sm:border-r sm:border-b-0"
          >
            {nav.map((s) => (
              <Fragment key={s.key}>
                {s.heading && (
                  <span className="text-muted-foreground hidden px-3 pt-3 pb-1 text-xs font-medium break-all sm:block">
                    {s.heading}
                  </span>
                )}
                <button
                  type="button"
                  aria-current={s.active ? "page" : undefined}
                  onClick={s.onSelect}
                  className={cn(
                    "focus-visible:ring-ring rounded-md px-3 py-2 text-left text-sm whitespace-nowrap outline-none focus-visible:ring-2",
                    s.active
                      ? "bg-accent text-foreground font-medium"
                      : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
                  )}
                >
                  {s.label}
                </button>
              </Fragment>
            ))}
          </nav>
          <div className="min-h-0 overflow-y-auto p-4">
            {app === null || section === "settings" ? settings : renderSection?.(section, app)}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
