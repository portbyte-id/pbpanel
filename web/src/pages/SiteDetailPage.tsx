import { Link, useNavigate } from "@tanstack/react-router"
import { ArrowLeft } from "lucide-react"
import { useState } from "react"

import { ApplyError } from "@/components/ApplyError"
import { ConfigFilePanel } from "@/components/ConfigFilePanel"
import { SiteControls } from "@/components/SiteControls"
import { SitePHP } from "@/components/SitePHP"
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ApiError, type SiteDetail } from "@/lib/api"
import { useDeleteSite, useRegenerateSite, useSite, useUpdateSite } from "@/lib/queries"
import { kindLabel } from "@/pages/SitesPage"

/** One site: what the panel controls, the raw file, and where the logs are. */
export function SiteDetailPage({ domain }: { domain: string }) {
  const { data, isPending, error } = useSite(domain)

  if (isPending) {
    return <p className="text-muted-foreground text-sm">Loading {domain}…</p>
  }
  if (error) {
    return (
      <div className="flex flex-col gap-3">
        <BackLink />
        <p role="alert" className="text-destructive text-sm">
          {error instanceof ApiError ? error.detail : "The site could not be loaded."}
        </p>
      </div>
    )
  }

  return (
    <div className="flex max-w-4xl flex-col gap-4">
      <BackLink />
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-xl font-semibold tracking-tight">{data.domain}</h2>
        <Badge variant="secondary">{kindLabel(data.kind)}</Badge>
      </div>

      <Tabs defaultValue="overview">
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          {data.kind === "php" && <TabsTrigger value="php">PHP</TabsTrigger>}
          <TabsTrigger value="config">Config</TabsTrigger>
          <TabsTrigger value="logs">Logs</TabsTrigger>
        </TabsList>
        <TabsContent value="overview">
          <Overview detail={data} />
        </TabsContent>
        {data.kind === "php" && (
          <TabsContent value="php">
            <SitePHP detail={data} />
          </TabsContent>
        )}
        <TabsContent value="config">
          <ConfigFilePanel source={{ kind: "site", domain: data.domain }} />
        </TabsContent>
        <TabsContent value="logs">
          <Logs domain={data.domain} php={data.kind === "php"} />
        </TabsContent>
      </Tabs>
    </div>
  )
}

function BackLink() {
  return (
    <Link
      to="/sites"
      className="text-muted-foreground hover:text-foreground flex w-fit items-center gap-1 text-sm"
    >
      <ArrowLeft className="size-4" aria-hidden="true" />
      All sites
    </Link>
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

      <DangerZone detail={detail} />
    </div>
  )
}

function DangerZone({ detail }: { detail: SiteDetail }) {
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
                  onSuccess: () => void navigate({ to: "/sites" }),
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
