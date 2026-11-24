import { SkeletonRows } from "@/components/ui/skeleton"
import { useId, useState } from "react"

import { ApplyError } from "@/components/ApplyError"
import { ConfigFilePanel } from "@/components/ConfigFilePanel"
import { ProxyRouteEditor, routesFromDrafts, type RouteDraft } from "@/components/CreateSiteDialog"
import { SiteControls } from "@/components/SiteControls"
import { SitePHP } from "@/components/SitePHP"
import { SiteTLS } from "@/components/SiteTLS"
import { SiteWAF, type WafPrefill } from "@/components/SiteWAF"
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
import { ApiError, type SiteDetail, type SiteRewrite } from "@/lib/api"
import type { LogSource } from "@/lib/logs"
import { cn } from "@/lib/utils"
import {
  type ConfigSource,
  useDeleteSite,
  useRegenerateSite,
  useSite,
  useSiteIncludes,
  useUpdateSite,
} from "@/lib/queries"
import { TailViewer } from "@/pages/LogsPage"
import { kindLabel } from "@/pages/SitesPage"

export type SiteSection = "domains" | "proxy" | "config" | "ssl" | "waf" | "php" | "rewrite" | "logs" | "delete"

/**
 * One site, as a dialog over the sites list. `/sites/<domain>` opens it, so a link
 * to a site keeps working. The menu and its sections are `SiteDetailBody`.
 */
export function SiteDetailDialog({
  domain,
  onClose,
  wafPrefill,
}: {
  domain: string
  onClose: () => void
  wafPrefill?: WafPrefill
}) {
  const { data } = useSite(domain)

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="h-[min(760px,90dvh)] grid-rows-[auto_minmax(0,1fr)] gap-0 overflow-hidden p-0 sm:max-w-[880px]">
        <DialogHeader className="border-b px-4 py-3 pr-12">
          <DialogTitle className="flex flex-wrap items-center gap-2">
            <span>Site modification [{domain}]</span>
            {data && <Badge variant="secondary">{kindLabel(data.kind)}</Badge>}
          </DialogTitle>
          <DialogDescription>
            {data ? `Added ${new Date(data.created_at).toLocaleString()}` : "Loading the site…"}
          </DialogDescription>
        </DialogHeader>
        <SiteDetailBody domain={domain} onGone={onClose} wafPrefill={wafPrefill} />
      </DialogContent>
    </Dialog>
  )
}

/** The sections of a site's configuration menu. PHP ones only exist for a PHP site. */
export function siteSectionList(php: boolean): { id: SiteSection; label: string }[] {
  return [
    { id: "domains", label: "Domain manager" },
    { id: "proxy", label: "Reverse proxy" },
    { id: "config", label: "Config" },
    { id: "ssl", label: "SSL" },
    { id: "waf", label: "Firewall" },
    ...(php ? [{ id: "php" as const, label: "PHP version" }] : []),
    ...(php ? [{ id: "rewrite" as const, label: "URL rewrite" }] : []),
    { id: "logs", label: "Logs" },
    { id: "delete", label: "Reset or delete" },
  ]
}

/** One section of a site's menu, for a site already loaded. */
function SiteSectionContent({
  detail,
  section,
  onGone,
  wafPrefill,
}: {
  detail: SiteDetail
  section: SiteSection
  onGone: () => void
  wafPrefill?: WafPrefill
}) {
  return (
    <>
      {section === "domains" && <Overview detail={detail} />}
      {section === "proxy" && <SiteProxyRoutes detail={detail} />}
      {section === "config" && <SiteConfig detail={detail} />}
      {section === "ssl" && <SiteTLS detail={detail} />}
      {section === "waf" && <SiteWAF domain={detail.domain} prefill={wafPrefill} />}
      {section === "php" && detail.php_version && <SitePHP detail={detail} />}
      {section === "rewrite" && detail.php_version && <SiteRewriteSection detail={detail} />}
      {section === "logs" && <SiteLogs domain={detail.domain} php={detail.php_version !== null} />}
      {section === "delete" && <DangerZone detail={detail} onGone={onGone} />}
    </>
  )
}

/**
 * One section of one site, loaded by domain. An app's dialog puts the site's menu
 * in its own sidebar and shows the section picked there through this.
 */
export function SitePane({
  domain,
  section,
  onGone,
}: {
  domain: string
  section: SiteSection
  onGone: () => void
}) {
  const { data, isPending, error } = useSite(domain)
  if (isPending) return <SkeletonRows label={`Loading ${domain}…`} />
  if (error) {
    return (
      <p role="alert" className="text-destructive text-sm">
        {error instanceof ApiError ? error.detail : "The site could not be loaded."}
      </p>
    )
  }
  return <SiteSectionContent key={`${domain}|${section}`} detail={data} section={section} onGone={onGone} />
}

/**
 * A site's configuration menu down the left and the section it shows: the body of
 * the site dialog. `onGone` runs after the site is deleted.
 *
 * `wafPrefill` is a rule handed over from the WAF audit viewer: the menu then
 * opens on the Firewall section with that rule ready to add as an exclusion.
 */
export function SiteDetailBody({
  domain,
  onGone,
  wafPrefill,
}: {
  domain: string
  onGone: () => void
  wafPrefill?: WafPrefill
}) {
  const { data, isPending, error } = useSite(domain)
  const [section, setSection] = useState<SiteSection>(wafPrefill ? "waf" : "domains")

  if (isPending) {
    return (
      <div className="p-4">
        <SkeletonRows label={`Loading ${domain}…`} />
      </div>
    )
  }
  if (error) {
    return (
      <p role="alert" className="text-destructive p-4 text-sm">
        {error instanceof ApiError ? error.detail : "The site could not be loaded."}
      </p>
    )
  }

  return (
    <div className="grid min-h-0 grid-cols-1 grid-rows-[auto_minmax(0,1fr)] sm:grid-cols-[11.5rem_minmax(0,1fr)] sm:grid-rows-1">
      <nav
        aria-label="Site sections"
        className="bg-muted/30 flex gap-1 overflow-x-auto border-b p-2 sm:flex-col sm:overflow-y-auto sm:border-r sm:border-b-0"
      >
        {siteSectionList(data.php_version !== null).map((s) => (
          <button
            key={s.id}
            type="button"
            aria-current={section === s.id ? "page" : undefined}
            onClick={() => setSection(s.id)}
            className={cn(
              "focus-visible:ring-ring rounded-md px-3 py-2 text-left text-sm whitespace-nowrap outline-none focus-visible:ring-2",
              section === s.id
                ? "bg-accent text-foreground font-medium"
                : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
            )}
          >
            {s.label}
          </button>
        ))}
      </nav>
      <div className="min-h-0 overflow-y-auto p-4">
        <SiteSectionContent detail={data} section={section} onGone={onGone} wafPrefill={wafPrefill} />
      </div>
    </div>
  )
}

function Overview({ detail }: { detail: SiteDetail }) {
  const update = useUpdateSite(detail.domain)
  const routes = detail.proxy_routes ?? []

  return (
    <div className="flex flex-col gap-8">
      <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
        {detail.root && (
          <>
            <dt className="text-muted-foreground">Folder</dt>
            <dd className="font-mono text-xs break-all">{detail.root}</dd>
          </>
        )}
        {detail.php_version && (
          <>
            <dt className="text-muted-foreground">PHP version</dt>
            <dd>{detail.php_version}</dd>
          </>
        )}
        <dt className="text-muted-foreground">Reverse proxy</dt>
        <dd>
          {routes.length === 0 ? (
            <span className="text-muted-foreground">No route</span>
          ) : (
            <ul className="flex flex-col gap-0.5 font-mono text-xs">
              {routes.map((r) => (
                <li key={r.path} className="break-all">
                  {r.path} → {r.target}
                  {r.strip_prefix ? " (prefix stripped)" : ""}
                </li>
              ))}
            </ul>
          )}
        </dd>
        <dt className="text-muted-foreground">Site file</dt>
        <dd className="font-mono text-xs break-all">
          {detail.site_file}
          {detail.site_file_missing && " (missing)"}
        </dd>
        <dt className="text-muted-foreground">Created</dt>
        <dd>{new Date(detail.created_at).toLocaleString()}</dd>
      </dl>

      {/* Keyed on the saved values so the inputs follow a save or a reload. */}
      <SiteControls
        key={detail.aliases.join(" ")}
        detail={detail}
        pending={update.isPending}
        error={update.error}
        onSave={(input) => update.mutate(input)}
      />
    </div>
  )
}

/**
 * The site's reverse-proxy routes, editable after creation.
 *
 * The list is the whole truth: saving replaces it, an empty list removes every
 * route. A route on "/" is refused by the server when the site also serves PHP
 * or a folder (both would claim `location /`), and the refusal says so.
 */
function SiteProxyRoutes({ detail }: { detail: SiteDetail }) {
  const update = useUpdateSite(detail.domain)
  const saved = detail.proxy_routes ?? []
  const [routes, setRoutes] = useState<RouteDraft[]>(() =>
    saved.map((r) => ({ path: r.path, target: r.target, strip_prefix: r.strip_prefix })),
  )
  const [done, setDone] = useState<string | null>(null)

  const next = routesFromDrafts(routes)
  const dirty = JSON.stringify(next) !== JSON.stringify(saved)
  const feature = detail.features.find((f) => f.feature === "proxy")
  const detached = feature !== undefined && !feature.attached

  return (
    <section aria-labelledby="proxy-heading" className="flex flex-col gap-4">
      <div>
        <h3 id="proxy-heading" className="text-sm font-semibold">
          Reverse proxy
        </h3>
        <p className="text-muted-foreground text-xs">
          Send a subpath, such as <code>/api</code>, to an app or another server. The rest of the
          site is served by its folder
          {detail.php_version ? " and PHP" : ""}.
        </p>
      </div>

      {detached ? (
        <p role="status" className="text-muted-foreground text-sm">
          Switched off: {feature.reason ?? "the site file no longer includes this control's file."}
        </p>
      ) : (
        <>
          <ProxyRouteEditor
            routes={routes}
            onChange={(r) => {
              setRoutes(r)
              setDone(null)
            }}
            error={update.error}
            disabled={update.isPending}
          />
          <div className="flex flex-wrap items-center gap-3">
            <Button
              disabled={!dirty || update.isPending}
              onClick={() =>
                update.mutate(
                  { proxy_routes: next },
                  { onSuccess: () => setDone("Saved. nginx reloaded.") },
                )
              }
            >
              {update.isPending ? "Saving…" : "Save routes"}
            </Button>
            <Button
              variant="outline"
              disabled={!dirty}
              onClick={() => {
                setRoutes(
                  saved.map((r) => ({ path: r.path, target: r.target, strip_prefix: r.strip_prefix })),
                )
                update.reset()
              }}
            >
              Discard
            </Button>
            <span className="text-muted-foreground text-xs" aria-live="polite">
              {done ?? ""}
            </span>
          </div>
          <ApplyError error={update.error} fallback="The routes could not be saved." />
        </>
      )}
    </section>
  )
}

function DangerZone({ detail, onGone }: { detail: SiteDetail; onGone: () => void }) {
  const regenerate = useRegenerateSite(detail.domain)
  const remove = useDeleteSite()
  const [dialog, setDialog] = useState<"regenerate" | "delete" | null>(null)
  const [typed, setTyped] = useState("")
  const [done, setDone] = useState<string | null>(null)

  const close = () => {
    setDialog(null)
    setTyped("")
    regenerate.reset()
    remove.reset()
  }

  return (
    <section aria-labelledby="danger-heading" className="flex flex-col gap-3">
      <h3 id="danger-heading" className="text-sm font-semibold">
        Reset or remove
      </h3>
      <p className="text-muted-foreground text-sm">
        Regenerating rewrites the site's configuration file from the panel's template. Deleting
        stops nginx answering for the site and removes its configuration.
      </p>
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="outline" onClick={() => setDialog("regenerate")}>
          Regenerate site file
        </Button>
        <Button variant="destructive" onClick={() => setDialog("delete")}>
          Delete site
        </Button>
        <span className="text-muted-foreground text-xs" aria-live="polite">
          {done ?? ""}
        </span>
      </div>

      <Dialog open={dialog === "regenerate"} onOpenChange={(open) => !open && close()}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Regenerate the site file?</DialogTitle>
            <DialogDescription>
              The file is rewritten from the panel's template and every edit you made to it is
              replaced. The current version stays in the file's history, so it can be restored.
              All controls are connected again afterwards.
            </DialogDescription>
          </DialogHeader>
          <ApplyError error={regenerate.error} fallback="The file could not be regenerated." />
          <DialogFooter>
            <Button variant="outline" onClick={close}>
              Keep my file
            </Button>
            <Button
              disabled={regenerate.isPending}
              onClick={() =>
                regenerate.mutate(undefined, {
                  onSuccess: () => {
                    close()
                    setDone("Site file regenerated.")
                  },
                })
              }
            >
              {regenerate.isPending ? "Regenerating…" : "Regenerate"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={dialog === "delete"} onOpenChange={(open) => !open && close()}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {detail.domain}?</DialogTitle>
            <DialogDescription>
              nginx stops answering for this site and its configuration is removed.
              {detail.kind !== "proxy" &&
                ` The files in ${detail.root} are kept; remove them yourself if you no longer need them.`}
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="confirm-domain">Type {detail.domain} to confirm</Label>
            <Input
              id="confirm-domain"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
          </div>
          <ApplyError error={remove.error} fallback="The site could not be deleted." />
          <DialogFooter>
            <Button variant="outline" onClick={close}>
              Keep site
            </Button>
            <Button
              variant="destructive"
              disabled={typed.trim() !== detail.domain || remove.isPending}
              onClick={() =>
                remove.mutate(detail.domain, { onSuccess: () => onGone() })
              }
            >
              {remove.isPending ? "Deleting…" : "Delete site"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  )
}

/**
 * The site's own logs, read through the log viewer's tail: the access log, the
 * nginx error log and, for a PHP site, the PHP error log. Every kind of site
 * writes the first two, so the menu is the same for a static, PHP, or proxied
 * site (an app's domain included).
 */
function SiteLogs({ domain, php }: { domain: string; php: boolean }) {
  const streams = [
    { id: "access", label: "Access log" },
    { id: "error", label: "Error log" },
    ...(php ? [{ id: "php", label: "PHP error log" }] : []),
  ]
  const [picked, setPicked] = useState("access")
  const current = streams.find((s) => s.id === picked) ?? streams[0]
  const source: LogSource = {
    id: `site:${domain}:${current.id}`,
    label: `${domain} ${current.label.toLowerCase()}`,
    kind: "site",
  }

  return (
    <div className="flex flex-col gap-4">
      <div role="tablist" aria-label="Log files" className="flex flex-wrap gap-1">
        {streams.map((s) => (
          <ConfigTab key={s.id} active={current.id === s.id} onClick={() => setPicked(s.id)}>
            {s.label}
          </ConfigTab>
        ))}
      </div>
      <TailViewer key={source.id} source={source} />
    </div>
  )
}

/**
 * The site's nginx configuration, every file of it. The first tab is the site's
 * own file; the rest are the panel-owned include files, each edited here and each
 * validated with `nginx -t` on save. This is how a reverse-proxy site's proxy_pass
 * block, or a site's listen and TLS directives, can be changed directly.
 */
function SiteConfig({ detail }: { detail: SiteDetail }) {
  const includes = useSiteIncludes(detail.domain)
  const [selected, setSelected] = useState("site")
  const list = includes.data?.includes ?? []

  const source: ConfigSource =
    selected === "site"
      ? { kind: "site", domain: detail.domain }
      : { kind: "site-include", domain: detail.domain, name: selected }

  return (
    <div className="flex flex-col gap-4">
      <div role="tablist" aria-label="Configuration files" className="flex flex-wrap gap-1">
        <ConfigTab active={selected === "site"} onClick={() => setSelected("site")}>
          Site file
        </ConfigTab>
        {list.map((inc) => (
          <ConfigTab
            key={inc.name}
            active={selected === inc.name}
            onClick={() => setSelected(inc.name)}
          >
            {inc.label}
            {inc.missing && <span className="text-muted-foreground"> (missing)</span>}
          </ConfigTab>
        ))}
      </div>
      <ConfigFilePanel key={selected} source={source} />
    </div>
  )
}

function ConfigTab({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={cn(
        "focus-visible:ring-ring rounded-md px-2.5 py-1.5 text-xs outline-none focus-visible:ring-2",
        active
          ? "bg-accent text-foreground font-medium"
          : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
      )}
    >
      {children}
    </button>
  )
}

const REWRITE_PRESETS: { value: SiteRewrite; label: string; tail: string }[] = [
  { value: "", label: "None (generic front controller)", tail: "/index.php?$query_string" },
  { value: "laravel", label: "Laravel", tail: "/index.php?$query_string" },
  { value: "wordpress", label: "WordPress", tail: "/index.php?$args" },
]

/**
 * The URL-rewrite preset of a PHP site. Changing it rewrites the panel-owned
 * `php.conf` include, which is validated with `nginx -t` before it goes live.
 */
function SiteRewriteSection({ detail }: { detail: SiteDetail }) {
  const id = useId()
  const update = useUpdateSite(detail.domain)
  const current: SiteRewrite = detail.rewrite ?? ""
  const [preset, setPreset] = useState<SiteRewrite>(current)
  const changed = preset !== current
  const tail = REWRITE_PRESETS.find((p) => p.value === preset)?.tail ?? ""

  return (
    <section aria-labelledby={`${id}-heading`} className="flex flex-col gap-4">
      <div>
        <h3 id={`${id}-heading`} className="text-sm font-semibold">
          URL rewrite
        </h3>
        <p className="text-muted-foreground text-xs">
          A request for a file that does not exist is passed to the app's front controller. Pick the
          preset that matches the app; saving rewrites the PHP include and checks it with nginx.
        </p>
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-preset`}>Preset</Label>
        <Select
          id={`${id}-preset`}
          value={preset}
          onChange={(e) => setPreset(e.target.value as SiteRewrite)}
          disabled={update.isPending}
        >
          {REWRITE_PRESETS.map((p) => (
            <option key={p.value} value={p.value}>
              {p.label}
            </option>
          ))}
        </Select>
        <p className="text-muted-foreground font-mono text-xs">
          try_files $uri $uri/ {tail};
        </p>
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <Button
          disabled={!changed || update.isPending}
          onClick={() => update.mutate({ rewrite: preset })}
        >
          {update.isPending ? "Saving…" : "Save and reload"}
        </Button>
        <Button variant="outline" disabled={!changed} onClick={() => setPreset(current)}>
          Discard
        </Button>
        <span className="text-muted-foreground text-xs" aria-live="polite">
          {update.isSuccess && !changed ? "Saved. nginx reloaded." : ""}
        </span>
      </div>
      <ApplyError error={update.error} fallback="The rewrite preset could not be saved." />
    </section>
  )
}
