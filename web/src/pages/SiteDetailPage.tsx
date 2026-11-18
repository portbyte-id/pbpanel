import { SkeletonRows } from "@/components/ui/skeleton"
import { useNavigate } from "@tanstack/react-router"
import { useState } from "react"

import { ApplyError } from "@/components/ApplyError"
import { ConfigFilePanel } from "@/components/ConfigFilePanel"
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
import { ApiError, type SiteDetail } from "@/lib/api"
import { cn } from "@/lib/utils"
import { useDeleteSite, useRegenerateSite, useSite, useUpdateSite } from "@/lib/queries"
import { kindLabel } from "@/pages/SitesPage"

type Section = "domains" | "config" | "ssl" | "waf" | "php" | "logs" | "delete"

/**
 * One site, as a dialog over the sites list: a rail of sections on the left, the
 * section on the right. It replaces the old page; `/sites/<domain>` still opens
 * it, so a link to a site keeps working.
 *
 * `wafPrefill` is a rule handed over from the WAF audit viewer: the dialog then
 * opens on the Firewall section with that rule ready to add as an exclusion.
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
  const { data, isPending, error } = useSite(domain)
  const [section, setSection] = useState<Section>(wafPrefill ? "waf" : "domains")

  const sections: { id: Section; label: string }[] = [
    { id: "domains", label: data?.kind === "proxy" ? "Domains & proxy" : "Domain manager" },
    { id: "config", label: "Config" },
    { id: "ssl", label: "SSL" },
    { id: "waf", label: "Firewall" },
    ...(data?.kind === "php" ? [{ id: "php" as const, label: "PHP version" }] : []),
    { id: "logs", label: "Response log" },
    { id: "delete", label: "Reset or delete" },
  ]

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

        {isPending ? (
          <div className="p-4">
            <SkeletonRows label={`Loading ${domain}…`} />
          </div>
        ) : error ? (
          <p role="alert" className="text-destructive p-4 text-sm">
            {error instanceof ApiError ? error.detail : "The site could not be loaded."}
          </p>
        ) : (
          <div className="grid min-h-0 grid-cols-1 grid-rows-[auto_minmax(0,1fr)] sm:grid-cols-[11.5rem_minmax(0,1fr)] sm:grid-rows-1">
            <nav
              aria-label="Site sections"
              className="bg-muted/30 flex gap-1 overflow-x-auto border-b p-2 sm:flex-col sm:overflow-y-auto sm:border-r sm:border-b-0"
            >
              {sections.map((s) => (
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
              {section === "domains" && <Overview detail={data} />}
              {section === "config" && <ConfigFilePanel source={{ kind: "site", domain: data.domain }} />}
              {section === "ssl" && <SiteTLS detail={data} />}
              {section === "waf" && <SiteWAF domain={data.domain} prefill={wafPrefill} />}
              {section === "php" && data.kind === "php" && <SitePHP detail={data} />}
              {section === "logs" && <Logs domain={data.domain} php={data.kind === "php"} />}
              {section === "delete" && <DangerZone detail={data} onGone={onClose} />}
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

function Overview({ detail }: { detail: SiteDetail }) {
  const update = useUpdateSite(detail.domain)

  return (
    <div className="flex flex-col gap-8">
      <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
        <dt className="text-muted-foreground">
          {detail.kind === "proxy" ? "Passes requests to" : "Folder"}
        </dt>
        <dd className="font-mono text-xs break-all">
          {detail.kind === "proxy" ? detail.proxy_target : detail.root}
        </dd>
        {detail.kind === "php" && (
          <>
            <dt className="text-muted-foreground">PHP version</dt>
            <dd>{detail.php_version}</dd>
          </>
        )}
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
        key={`${detail.aliases.join(" ")}|${detail.proxy_target ?? ""}`}
        detail={detail}
        pending={update.isPending}
        error={update.error}
        onSave={(input) => update.mutate(input)}
      />
    </div>
  )
}

function DangerZone({ detail, onGone }: { detail: SiteDetail; onGone: () => void }) {
  const navigate = useNavigate()
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
                remove.mutate(detail.domain, {
                  onSuccess: () => {
                    onGone()
                    void navigate({ to: "/sites" })
                  },
                })
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

function Logs({ domain, php }: { domain: string; php: boolean }) {
  return (
    <div className="flex flex-col gap-2 text-sm">
      <p>The panel does not show logs yet. Until it does, they are on the server at:</p>
      <ul className="font-mono text-xs">
        <li>/www/panel/logs/sites/{domain}.access.log</li>
        <li>/www/panel/logs/sites/{domain}.error.log</li>
        {php && <li>/www/panel/logs/sites/{domain}.php.log</li>}
      </ul>
    </div>
  )
}
