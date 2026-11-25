import { SkeletonRows } from "@/components/ui/skeleton"
import { useId, useState } from "react"

import { ApplyError } from "@/components/ApplyError"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { JobsDrawer } from "@/components/JobsDrawer"
import { IssueCertForm, TLSWarnings, UploadCertForm } from "@/components/TLSForms"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  ApiError,
  type CertView,
  type DNSProvider,
  type SiteDetail,
  type TLSWarning,
} from "@/lib/api"
import {
  useIssueSiteCert,
  useRefreshTLS,
  useRemoveSiteCert,
  useRenewSiteCert,
  useSetSiteRedirect,
  useSiteTLS,
  useTLSProviders,
  useUploadSiteCert,
} from "@/lib/queries"
import { canRenew, expiryText, expiryTone, formatDate, sourceLabel } from "@/lib/tls"

/** The expiry as a badge: amber inside 14 days, red once expired. */
export function ExpiryBadge({ daysLeft }: { daysLeft: number }) {
  const tone = expiryTone(daysLeft)
  return (
    <Badge
      variant={tone === "expired" ? "destructive" : tone === "warn" ? "warning" : "outline"}
      data-tone={tone}
    >
      {expiryText(daysLeft)}
    </Badge>
  )
}

/**
 * The TLS tab of a site, in three tabs: the certificate it serves, a
 * certificate from a pre-configured ACME provider, or the operator's own.
 *
 * The tab list stays clickable when the include is detached; only the controls
 * inside each tab are disabled, so the reason is readable in every tab.
 */
export function SiteTLS({ detail }: { detail: SiteDetail }) {
  const id = useId()
  const domain = detail.domain
  const tls = useSiteTLS(domain)
  const providers = useTLSProviders()
  const feature = detail.features.find((f) => f.feature === "tls")
  const detached = feature !== undefined && !feature.attached

  if (tls.isPending || providers.isPending) {
    return <SkeletonRows label="Loading the certificate…" />
  }
  if (tls.error) {
    return (
      <p role="alert" className="text-destructive text-sm">
        {tls.error instanceof ApiError ? tls.error.detail : "The certificate could not be loaded."}
      </p>
    )
  }

  const cert = tls.data ?? null

  return (
    <div className="flex flex-col gap-4">
      {detached && (
        <p id={`${id}-detached`} role="status" className="text-muted-foreground text-sm">
          Switched off: {feature.reason ?? "the site file no longer includes this control's file."}{" "}
          The controls below do nothing until the include line is back.
        </p>
      )}
      <Tabs defaultValue="details">
        <TabsList aria-label={`TLS for ${domain}`}>
          <TabsTrigger value="details">Details</TabsTrigger>
          <TabsTrigger value="acme">Create with ACME</TabsTrigger>
          <TabsTrigger value="own">Own certificate</TabsTrigger>
        </TabsList>
        {/* A disabled fieldset disables every control inside it, buttons included. */}
        <TabsContent value="details">
          <fieldset disabled={detached} className="flex min-w-0 flex-col gap-8 pt-2">
            <legend className="sr-only">Certificate for {domain}</legend>
            <Current domain={domain} cert={cert} />
          </fieldset>
        </TabsContent>
        <TabsContent value="acme">
          <fieldset disabled={detached} className="flex min-w-0 flex-col gap-8 pt-2">
            <legend className="sr-only">Create a certificate with ACME for {domain}</legend>
            <AcmeCertificate
              detail={detail}
              cert={cert}
              providers={providers.data?.providers ?? []}
            />
          </fieldset>
        </TabsContent>
        <TabsContent value="own">
          <fieldset disabled={detached} className="flex min-w-0 flex-col gap-8 pt-2">
            <legend className="sr-only">Own certificate for {domain}</legend>
            <OwnCertificate domain={domain} cert={cert} />
          </fieldset>
        </TabsContent>
      </Tabs>
    </div>
  )
}

function Current({ domain, cert }: { domain: string; cert: CertView | null }) {
  const id = useId()
  const [jobId, setJobId] = useState<string | null>(null)
  const [removing, setRemoving] = useState(false)
  const refresh = useRefreshTLS()
  const renew = useRenewSiteCert(domain)
  const redirect = useSetSiteRedirect(domain)
  const remove = useRemoveSiteCert(domain)

  return (
    <section aria-labelledby={`${id}-heading`} className="flex flex-col gap-4">
      <h3 id={`${id}-heading`} className="text-sm font-semibold">
        Current certificate
      </h3>

      {cert === null ? (
        <p className="text-sm">
          This site has no certificate, so {domain} is served over plain HTTP only. Create one
          with ACME or use your own in the other tabs.
        </p>
      ) : (
        <>
          <dl className="grid grid-cols-[max-content_1fr] items-baseline gap-x-6 gap-y-2 text-sm">
            <dt className="text-muted-foreground">Expires</dt>
            <dd className="flex flex-wrap items-center gap-2">
              {formatDate(cert.not_after)} <ExpiryBadge daysLeft={cert.days_left} />
            </dd>
            <dt className="text-muted-foreground">Source</dt>
            <dd>
              <Badge variant="secondary">{sourceLabel(cert.source)}</Badge>
            </dd>
            <dt className="text-muted-foreground">Issuer</dt>
            <dd className="break-all">{cert.issuer}</dd>
            <dt className="text-muted-foreground">Names</dt>
            <dd className="font-mono text-xs break-all">{cert.names.join(", ")}</dd>
            <dt className="text-muted-foreground">Valid from</dt>
            <dd>{formatDate(cert.not_before)}</dd>
            <dt className="text-muted-foreground">Renewal</dt>
            <dd>
              <RenewalLine cert={cert} />
            </dd>
          </dl>

          <div className="flex flex-wrap items-center gap-3">
            <Button
              variant="outline"
              disabled={!canRenew(cert) || renew.isPending}
              aria-describedby={canRenew(cert) ? undefined : `${id}-renew-why`}
              onClick={() => renew.mutate(undefined, { onSuccess: setJobId })}
            >
              {renew.isPending ? "Starting…" : "Renew now"}
            </Button>
            <Button variant="destructive" onClick={() => setRemoving(true)}>
              Remove certificate
            </Button>
          </div>
          {!canRenew(cert) && (
            <p id={`${id}-renew-why`} className="text-muted-foreground text-xs">
              An uploaded certificate can't be renewed here. Upload a new one in the Own certificate
              tab, or create one with ACME.
            </p>
          )}
          <ApplyError error={renew.error} fallback="The renewal could not be started." />

          <div className="flex flex-col gap-1.5">
            <label className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                checked={cert.redirect_http}
                disabled={redirect.isPending}
                onChange={(e) => redirect.mutate(e.target.checked)}
                aria-describedby={`${id}-redirect-hint`}
                className="mt-0.5"
              />
              <span className="font-medium">Redirect HTTP to HTTPS</span>
            </label>
            <p id={`${id}-redirect-hint`} className="text-muted-foreground pl-6 text-xs">
              Visitors on port 80 are sent to the https address. Requests under
              /.well-known/acme-challenge/ stay on HTTP, so HTTP-01 issuance and renewal keep
              working with the redirect on.
            </p>
            <div className="pl-6">
              <ApplyError error={redirect.error} fallback="The redirect could not be changed." />
            </div>
          </div>
        </>
      )}

      <ConfirmDialog
        open={removing}
        onOpenChange={(open) => {
          setRemoving(open)
          if (!open) remove.reset()
        }}
        title={`Remove the certificate for ${domain}?`}
        confirmLabel="Remove certificate"
        pendingLabel="Removing…"
        pending={remove.isPending}
        destructive
        error={remove.error instanceof ApiError ? remove.error.detail : remove.error ? "The certificate could not be removed." : null}
        onConfirm={() => remove.mutate(undefined, { onSuccess: () => setRemoving(false) })}
      >
        <p>
          The site goes back to plain HTTP: nginx stops serving HTTPS for {domain} and the
          redirect is turned off. The certificate and its private key are deleted from the
          panel.
        </p>
        <p>Visitors who already use the https address will get a connection error.</p>
      </ConfirmDialog>

      <JobsDrawer
        jobId={jobId}
        onClose={() => {
          setJobId(null)
          refresh()
        }}
      />
    </section>
  )
}

function RenewalLine({ cert }: { cert: CertView }) {
  if (cert.source === "upload") {
    return <span>Not renewed by the panel. Upload a new certificate before it expires.</span>
  }
  const when = cert.last_renewal_at ? formatDate(cert.last_renewal_at) : null
  if (cert.last_renewal_result === "failed") {
    return (
      <span className="flex flex-col gap-1">
        <span className="text-destructive font-medium">
          Last renewal failed{when ? ` on ${when}` : ""}.
        </span>
        {cert.last_renewal_error && (
          <code translate="no" className="text-xs break-words whitespace-pre-wrap">{cert.last_renewal_error}</code>
        )}
        <span className="text-muted-foreground text-xs">The panel retries automatically.</span>
      </span>
    )
  }
  if (cert.last_renewal_result === "ok") {
    return <span>Renewed{when ? ` on ${when}` : ""}. The panel renews it automatically.</span>
  }
  return <span>Not renewed yet. The panel renews it automatically when it is due.</span>
}

/** Requests a certificate from an ACME provider, with the names the site already serves. */
function AcmeCertificate({
  detail,
  cert,
  providers,
}: {
  detail: SiteDetail
  cert: CertView | null
  providers: DNSProvider[]
}) {
  const id = useId()
  const domain = detail.domain
  const [jobId, setJobId] = useState<string | null>(null)
  const refresh = useRefreshTLS()
  const issue = useIssueSiteCert(domain)
  const [reused, setReused] = useState(false)
  const verb = cert ? "Replace" : "Create"

  return (
    <>
      <section aria-labelledby={`${id}-issue`} className="flex flex-col gap-3">
        <h3 id={`${id}-issue`} className="text-sm font-semibold">
          {verb} a certificate with ACME
        </h3>
        <p className="text-muted-foreground text-xs">
          Pick the provider and the names. If a certificate already stored covers these names — an
          uploaded wildcard, or one for another site — it is used as it is instead of a new order;
          otherwise the panel renews the certificate it issues before it expires.
        </p>
        {reused && (
          <p role="status" className="text-sm">
            A certificate already in the panel covers these names, so it was put in place. No new
            order was placed.
          </p>
        )}
        <IssueCertForm
          domain={domain}
          defaultNames={[domain, ...detail.aliases]}
          providers={providers}
          pending={issue.isPending}
          error={issue.error}
          onSubmit={(input) => {
            setReused(false)
            issue.mutate(input, {
              onSuccess: (id) => {
                if (id === null) {
                  setReused(true)
                  refresh()
                } else {
                  setJobId(id)
                }
              },
            })
          }}
        />
      </section>

      <JobsDrawer
        jobId={jobId}
        onClose={() => {
          setJobId(null)
          refresh()
        }}
      />
    </>
  )
}

/** Uploads a certificate and its key from the operator's own files. */
function OwnCertificate({ domain, cert }: { domain: string; cert: CertView | null }) {
  const id = useId()
  const [warnings, setWarnings] = useState<TLSWarning[]>([])
  const [uploaded, setUploaded] = useState(false)
  const upload = useUploadSiteCert(domain)

  return (
    <section aria-labelledby={`${id}-upload`} className="flex flex-col gap-3">
      <h3 id={`${id}-upload`} className="text-sm font-semibold">
        {cert ? "Replace it with your own certificate" : "Use your own certificate"}
      </h3>
      <p className="text-muted-foreground text-xs">
        The panel does not renew an uploaded certificate. Upload a new one before it expires.
      </p>
      <UploadCertForm
        pending={upload.isPending}
        error={upload.error}
        onSubmit={(input) => {
          setUploaded(false)
          upload.mutate(input, {
            onSuccess: (res) => {
              setWarnings(res.warnings ?? [])
              setUploaded(true)
            },
          })
        }}
      />
      {uploaded && (
        <p role="status" className="text-sm">
          Certificate saved.
        </p>
      )}
      <TLSWarnings warnings={warnings} />
    </section>
  )
}
