import { useId, useState } from "react"
import { Plus, Trash2 } from "lucide-react"

import { ApplyError } from "@/components/ApplyError"
import { FolderPicker } from "@/components/FolderPicker"
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
  type CreateSiteInput,
  type PanelApp,
  type ProxyRoute,
  type SiteRewrite,
} from "@/lib/api"
import { useApps, useCreateSite, usePHP } from "@/lib/queries"

/** Aliases are typed as one line or several; commas, spaces and newlines all separate. */
export function parseAliases(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s !== "")
}

/** A route row as the dialog edits it. `path` is empty for a row not filled in yet. */
export interface RouteDraft {
  path: string
  target: string
  strip_prefix: boolean
}

export function newRouteDraft(target = ""): RouteDraft {
  return { path: "", target, strip_prefix: true }
}

/**
 * The routes to send, dropping an entirely empty row. `strip_prefix` defaults to
 * on; the API applies the same default, but sending it explicitly keeps the
 * dialog's checkbox honest.
 */
export function routesFromDrafts(drafts: RouteDraft[]): ProxyRoute[] {
  return drafts
    .filter((d) => d.path.trim() !== "" || d.target.trim() !== "")
    .map((d) => ({
      path: d.path.trim(),
      target: d.target.trim(),
      strip_prefix: d.strip_prefix,
    }))
}

/**
 * The editable list of `path -> target` routes. It is controlled: the caller owns
 * the rows, so a submit sends exactly what is on screen. Each row has a remove
 * control and a strip-prefix toggle. A server refusal is shown once under the
 * list, because the server reports the field (`proxy_routes`), not one row.
 */
export function ProxyRouteEditor({
  routes,
  onChange,
  disabled = false,
  error = null,
  id: idProp,
}: {
  routes: RouteDraft[]
  onChange: (routes: RouteDraft[]) => void
  disabled?: boolean
  /** A server refusal for the whole list, or a field-level one. */
  error?: unknown
  id?: string
}) {
  const auto = useId()
  const id = idProp ?? auto
  const field = error instanceof ApiError && error.code === "invalid_input" ? error.field : null
  const routeError = field === "proxy_routes" || field === "proxy_target" ? error : null

  const update = (index: number, patch: Partial<RouteDraft>) =>
    onChange(routes.map((r, i) => (i === index ? { ...r, ...patch } : r)))
  const remove = (index: number) => onChange(routes.filter((_, i) => i !== index))
  const add = () => onChange([...routes, newRouteDraft()])

  return (
    <div className="flex flex-col gap-3" data-testid="proxy-route-editor">
      {routes.length === 0 ? (
        <p className="text-muted-foreground text-sm">
          No routes yet. The whole site is served as above; add a route to send one subpath, such
          as <code translate="no">/api</code>, to an app or another server.
        </p>
      ) : (
        <ul className="flex flex-col gap-3">
          {routes.map((route, i) => {
            const rowInvalid = field === "proxy_routes" && route.path.trim() === ""
            return (
              <li key={i} className="flex flex-col gap-2 rounded-md border p-3">
                <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
                  <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                    <Label htmlFor={`${id}-path-${i}`}>Path</Label>
                    <Input
                      id={`${id}-path-${i}`}
                      value={route.path}
                      onChange={(e) => update(i, { path: e.target.value })}
                      placeholder="/api…"
                      autoComplete="off"
                      spellCheck={false}
                      disabled={disabled}
                      aria-invalid={rowInvalid}
                    />
                  </div>
                  <div className="flex min-w-0 flex-[2] flex-col gap-1.5">
                    <Label htmlFor={`${id}-target-${i}`}>Pass requests to</Label>
                    <Input
                      id={`${id}-target-${i}`}
                      value={route.target}
                      onChange={(e) => update(i, { target: e.target.value })}
                      placeholder="http://127.0.0.1:3000…"
                      autoComplete="off"
                      spellCheck={false}
                      disabled={disabled}
                    />
                  </div>
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    onClick={() => remove(i)}
                    disabled={disabled}
                    aria-label={`Remove route ${route.path || i + 1}`}
                  >
                    <Trash2 aria-hidden="true" />
                  </Button>
                </div>
                <label className="flex items-start gap-2 text-xs">
                  <input
                    type="checkbox"
                    checked={route.strip_prefix}
                    onChange={(e) => update(i, { strip_prefix: e.target.checked })}
                    disabled={disabled}
                    className="mt-0.5"
                  />
                  <span className="flex flex-col gap-0.5">
                    Strip the prefix
                    <span className="text-muted-foreground">
                      On: <code translate="no">/api/users</code> reaches the app as <code translate="no">/users</code>. Off: it
                      arrives as <code translate="no">/api/users</code>.
                    </span>
                  </span>
                </label>
              </li>
            )
          })}
        </ul>
      )}
      <div className="flex items-center gap-2">
        <Button type="button" variant="outline" onClick={add} disabled={disabled}>
          <Plus aria-hidden="true" />
          Add route
        </Button>
      </div>
      {routeError instanceof ApiError && (
        <p role="alert" className="text-destructive text-xs">
          {fieldErrorDetail(routeError)}
        </p>
      )}
    </div>
  )
}

/** The sentence of a field-level error, without the field's own name prefix. */
function fieldErrorDetail(err: ApiError): string {
  return err.field === null ? err.detail : err.detail.replace(`${err.field}: `, "")
}

/**
 * The create form, without the dialog or the request around it, so its behaviour
 * is tested as a form.
 *
 * ONE site, one dialog. A site may serve static files, run PHP, and proxy
 * subpaths at once, so the PHP section and the reverse-proxy section are both
 * optional and both may be filled in. `kind` only decides what the list calls the
 * site and how nginx answers a request no route claims; it is derived here so the
 * operator never has to pick a "type" that limits them:
 *   - a site that runs PHP is `php`;
 *   - a site with no folder and only routes is `proxy`;
 *   - every other site serves files from its folder (`static`).
 *
 * A `/` route next to PHP or a folder is refused by the server: both would claim
 * `location /`. The form offers "/" only for a pure proxy site.
 */
export function CreateSiteForm({
  onSubmit,
  onCancel,
  pending,
  error,
  phpVersions = [],
  apps = [],
  initial,
}: {
  /** The apps that have a port, offered as route targets. */
  apps?: PanelApp[]
  /** Starting values, for a site created from elsewhere (an app's "Proxy site"). */
  initial?: { target: string }
  onSubmit: (input: CreateSiteInput) => void
  onCancel: () => void
  pending: boolean
  error: unknown
  /** The installed PHP versions. With none, the PHP section says how to get one. */
  phpVersions?: string[]
}) {
  const id = useId()
  const [domain, setDomain] = useState("")
  const [aliases, setAliases] = useState("")
  const [root, setRoot] = useState("")
  const [picking, setPicking] = useState(false)
  // A site serves files from a folder unless the operator says it is a pure
  // proxy; enabling PHP turns the folder back on.
  const [serveFiles, setServeFiles] = useState(true)
  const [phpOn, setPHPOn] = useState(false)
  // The newest installed version is the default.
  const [phpVersion, setPHPVersion] = useState(phpVersions[phpVersions.length - 1] ?? "")
  const [phpinfo, setPHPInfo] = useState(false)
  const [rewrite, setRewrite] = useState<SiteRewrite>("")
  // A route seeded from an app's "Proxy site" link starts as a subpath, which is
  // the safe shape: "/" would fight PHP or the folder.
  const [routes, setRoutes] = useState<RouteDraft[]>(() =>
    initial?.target ? [{ path: "/api", target: initial.target, strip_prefix: true }] : [],
  )

  const noPHPInstalled = phpOn && phpVersions.length === 0
  const hasPHP = phpOn && phpVersions.length > 0

  const field = error instanceof ApiError && error.code === "invalid_input" ? error.field : null
  const fieldError = (name: string) =>
    field === name && error instanceof ApiError ? (
      <p id={`${id}-${name}-error`} role="alert" className="text-destructive text-xs">
        {error.detail.replace(`${name}: `, "")}
      </p>
    ) : null
  const describedBy = (name: string, hint?: string) =>
    [field === name ? `${id}-${name}-error` : null, hint ? `${id}-${name}-hint` : null]
      .filter(Boolean)
      .join(" ") || undefined

  const sendsFiles = serveFiles || hasPHP
  const draftRoutes = routesFromDrafts(routes)
  // A pure proxy site: no folder, no PHP, at least one route. Only then may a
  // route be "/", because nothing else claims that location.
  const pureProxy = !sendsFiles && draftRoutes.length > 0

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    const input: CreateSiteInput = {
      domain: domain.trim(),
      aliases: parseAliases(aliases),
      kind: hasPHP ? "php" : pureProxy ? "proxy" : "static",
    }
    if (sendsFiles && root.trim() !== "") input.root = root.trim()
    if (hasPHP) {
      input.php_version = phpVersion
      if (phpinfo) input.phpinfo = true
      if (rewrite !== "") input.rewrite = rewrite
    }
    if (draftRoutes.length > 0) input.proxy_routes = draftRoutes
    onSubmit(input)
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-5" noValidate>
      <fieldset className="flex flex-col gap-3">
        <legend className="sr-only">The site</legend>

        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-domain`}>Domain</Label>
          <Input
            id={`${id}-domain`}
            value={domain}
            onChange={(e) => setDomain(e.target.value)}
            placeholder="example.com…"
            autoComplete="off"
            spellCheck={false}
            required
            aria-invalid={field === "domain"}
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
            placeholder="www.example.com…"
            autoComplete="off"
            spellCheck={false}
            aria-invalid={field === "aliases"}
            aria-describedby={describedBy("aliases", "hint")}
          />
          <p id={`${id}-aliases-hint`} className="text-muted-foreground text-xs">
            Optional. Separate several with spaces or commas.
          </p>
          {fieldError("aliases")}
        </div>
      </fieldset>

      {/* Files: the folder the site serves, unless it is a pure proxy site. */}
      <section aria-labelledby={`${id}-files-heading`} className="flex flex-col gap-3">
        <h3 id={`${id}-files-heading`} className="text-sm font-semibold">
          Files
        </h3>
        {!hasPHP && (
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={serveFiles}
              onChange={(e) => setServeFiles(e.target.checked)}
              className="mt-0.5"
            />
            <span className="flex flex-col gap-0.5">
              Serve files from a folder
              <span className="text-muted-foreground text-xs">
                Off makes this a pure reverse proxy: every request goes to a route below.
              </span>
            </span>
          </label>
        )}
        {sendsFiles && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-root`}>Folder</Label>
            <div className="flex flex-col gap-2 sm:flex-row">
              <Input
                id={`${id}-root`}
                value={root}
                onChange={(e) => setRoot(e.target.value)}
                placeholder={`/www/services/sites/${domain.trim() || "example.com"}`}
                autoComplete="off"
                spellCheck={false}
                aria-invalid={field === "root"}
                aria-describedby={describedBy("root", "hint")}
              />
              <Button type="button" variant="outline" onClick={() => setPicking(true)}>
                Browse…
              </Button>
            </div>
            <FolderPicker
              open={picking}
              onOpenChange={setPicking}
              start={root.trim() || undefined}
              onPick={setRoot}
            />
            <p id={`${id}-root-hint`} className="text-muted-foreground text-xs">
              Leave empty to use the folder shown. It is created with a placeholder page if it does
              not exist, and must be inside /www/services/sites or a folder allowed on the Settings
              page under Sites.
            </p>
            {fieldError("root")}
          </div>
        )}
      </section>

      {/* PHP: any site with a folder may run it, whatever its kind. */}
      <section aria-labelledby={`${id}-php-heading`} className="flex flex-col gap-3">
        <h3 id={`${id}-php-heading`} className="text-sm font-semibold">
          PHP
        </h3>
        <label className="flex items-start gap-2 text-sm">
          <input
            type="checkbox"
            checked={phpOn}
            onChange={(e) => setPHPOn(e.target.checked)}
            className="mt-0.5"
          />
          <span className="flex flex-col gap-0.5">
            Run PHP on this site
            <span className="text-muted-foreground text-xs">
              Scripts in the folder are run by PHP-FPM. The site may still proxy subpaths.
            </span>
          </span>
        </label>

        {noPHPInstalled && (
          <p role="status" className="text-sm">
            No PHP version is installed yet. Install one on the PHP page, then come back.
          </p>
        )}

        {hasPHP && (
          <div className="flex flex-col gap-3">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-php_version`}>PHP version</Label>
              <Select
                id={`${id}-php_version`}
                value={phpVersion}
                onChange={(e) => setPHPVersion(e.target.value)}
                aria-invalid={field === "php_version"}
                aria-describedby={describedBy("php_version")}
              >
                {phpVersions.map((v) => (
                  <option key={v} value={v}>
                    PHP {v}
                  </option>
                ))}
              </Select>
              {fieldError("php_version")}
            </div>
            <label className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                checked={phpinfo}
                onChange={(e) => setPHPInfo(e.target.checked)}
                className="mt-0.5"
              />
              <span className="flex flex-col gap-0.5">
                Start with a phpinfo() page
                <span className="text-muted-foreground text-xs">
                  It shows visitors the server's whole PHP setup. Use it to check the site works,
                  then delete index.php.
                </span>
              </span>
            </label>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-rewrite`}>URL rewrite</Label>
              <Select
                id={`${id}-rewrite`}
                value={rewrite}
                onChange={(e) => setRewrite(e.target.value as SiteRewrite)}
              >
                <option value="">None (generic front controller)</option>
                <option value="laravel">Laravel</option>
                <option value="wordpress">WordPress</option>
              </Select>
              <p className="text-muted-foreground text-xs">
                Sends a request for a file that does not exist to index.php with the app's query
                string. You can change it later on the site's URL Rewrite tab.
              </p>
              {fieldError("rewrite")}
            </div>
          </div>
        )}
      </section>

      {/* Reverse proxy: one optional section, one row per subpath. */}
      <section aria-labelledby={`${id}-proxy-heading`} className="flex flex-col gap-3">
        <div className="flex flex-col gap-1">
          <h3 id={`${id}-proxy-heading`} className="text-sm font-semibold">
            Reverse proxy
          </h3>
          <p className="text-muted-foreground text-xs">
            Send a subpath, such as <code translate="no">/api</code>, to an app or another server while the rest
            of the site is served normally.
          </p>
        </div>
        {apps.length > 0 && (
          <div className="flex flex-wrap gap-2">
            {apps.map((a) => (
              <Button
                key={a.name}
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setRoutes([...routes, newRouteDraft(a.proxy_url)])}
              >
                {a.name} (port {a.port})
              </Button>
            ))}
          </div>
        )}
        <ProxyRouteEditor
          routes={routes}
          onChange={setRoutes}
          error={error}
          disabled={pending}
          id={`${id}-routes`}
        />
      </section>

      {field === null && <ApplyError error={error} fallback="The site could not be created." />}

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={pending || domain.trim() === "" || noPHPInstalled}>
          {pending ? "Creating…" : "Create site"}
        </Button>
      </DialogFooter>
    </form>
  )
}

export function CreateSiteDialog({
  open,
  onOpenChange,
  onCreated,
  initial,
}: {
  initial?: { target: string }
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (domain: string) => void
}) {
  const create = useCreateSite()
  const php = usePHP()
  const appsList = (useApps().data ?? []).filter((a) => a.proxy_url !== "")
  const ready = (php.data?.versions ?? []).filter((v) => v.state === "ready").map((v) => v.version)

  const close = (next: boolean) => {
    if (!next) create.reset()
    onOpenChange(next)
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Add a site</DialogTitle>
          <DialogDescription>
            One site can serve files, run PHP, and pass subpaths to apps — fill in the parts it
            needs. The panel writes the nginx configuration and checks it before nginx reloads; if
            nginx refuses it, nothing is changed.
          </DialogDescription>
        </DialogHeader>
        {/* Remounted on open, so a closed dialog does not reopen with old text. */}
        {open && (
          <CreateSiteForm
            // Remounted when the versions arrive, so the picker starts on one.
            key={ready.join(",")}
            initial={initial}
            phpVersions={ready}
            apps={appsList}
            pending={create.isPending}
            error={create.error}
            onCancel={() => close(false)}
            onSubmit={(input) =>
              create.mutate(input, {
                onSuccess: (res) => {
                  close(false)
                  onCreated(res.site.domain)
                },
              })
            }
          />
        )}
      </DialogContent>
    </Dialog>
  )
}
