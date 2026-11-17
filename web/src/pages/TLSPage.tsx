import { Link } from "@tanstack/react-router"
import { useState, type ReactNode } from "react"

import { ConfirmDialog } from "@/components/ConfirmDialog"
import { JobsDrawer } from "@/components/JobsDrawer"
import { ExpiryBadge } from "@/components/SiteTLS"
import {
  PanelIssueForm,
  ProviderForm,
  UploadCertForm,
} from "@/components/TLSForms"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { ApiError, type CertInfo, type CertView, type DNSProvider } from "@/lib/api"
import {
  useDeleteTLSProvider,
  useIssuePanelCert,
  usePanelCert,
  useRefreshTLS,
  useSaveTLSProvider,
  useTLSCerts,
  useTLSProviders,
  useUploadPanelCert,
} from "@/lib/queries"
import { formatDate, renewalStatus, sortCertsBySoonest, sourceLabel } from "@/lib/tls"

/** Every certificate, the one that needs attention first; DNS providers; the panel's own certificate. */
export function TLSPage() {
  return (
    <div className="flex max-w-4xl flex-col gap-6">
      <CertificatesCard />
      <ProvidersCard />
      <PanelCertCard />
    </div>
  )
}

function errorText(error: unknown, fallback: string): string {
  return error instanceof ApiError ? error.detail : fallback
}

/* --- Certificates ------------------------------------------------------- */

function CertificatesCard() {
  const { data, isPending, error } = useTLSCerts()
  return (
    <Card>
      <CardHeader>
        <CardTitle>Site certificates</CardTitle>
        <CardDescription>Soonest expiry first. Open a site to renew, replace or remove its certificate.</CardDescription>
      </CardHeader>
      <CardContent>
        {isPending ? (
          <p className="text-muted-foreground text-sm">Loading certificates…</p>
        ) : error ? (
          <p role="alert" className="text-destructive text-sm">
            {errorText(error, "The certificates could not be loaded.")}
          </p>
        ) : (
          <CertificateTable
            certs={data}
            renderDomain={(domain) => (
              <Link
                to="/sites/$domain"
                params={{ domain }}
                className="font-medium underline-offset-2 hover:underline"
              >
                {domain}
              </Link>
            )}
          />
        )}
      </CardContent>
    </Card>
  )
}

/** The certificate list. `renderDomain` supplies the link to the site. */
export function CertificateTable({
  certs,
  renderDomain,
}: {
  certs: CertView[]
  renderDomain: (domain: string) => ReactNode
}) {
  if (certs.length === 0) {
    return (
      <p className="text-sm">
        No site has a certificate yet. Open a site and use its TLS tab to request or upload one.
      </p>
    )
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-sm">
        <thead className="text-muted-foreground text-xs">
          <tr>
            <th scope="col" className="py-2 pr-4 font-medium">Site</th>
            <th scope="col" className="py-2 pr-4 font-medium">Expires</th>
            <th scope="col" className="py-2 pr-4 font-medium">Source</th>
            <th scope="col" className="py-2 font-medium">Renewal</th>
          </tr>
        </thead>
        <tbody className="divide-border divide-y">
          {sortCertsBySoonest(certs).map((c) => (
            <tr key={c.domain} data-domain={c.domain}>
              <td className="py-2 pr-4">{renderDomain(c.domain)}</td>
              <td className="py-2 pr-4">
                <div className="flex flex-wrap items-center gap-2">
                  <span>{formatDate(c.not_after)}</span>
                  <ExpiryBadge daysLeft={c.days_left} />
                </div>
              </td>
              <td className="py-2 pr-4">
                <Badge variant="secondary">{sourceLabel(c.source)}</Badge>
              </td>
              <td className="py-2">
                <span className={c.last_renewal_result === "failed" ? "text-destructive" : undefined}>
                  {renewalStatus(c)}
                </span>
                {c.last_renewal_result === "failed" && c.last_renewal_error && (
                  <span className="text-muted-foreground block text-xs break-words">
                    {c.last_renewal_error}
                  </span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/* --- DNS providers ------------------------------------------------------ */

function ProvidersCard() {
  const list = useTLSProviders()
  const save = useSaveTLSProvider()
  const remove = useDeleteTLSProvider()
  const [replacing, setReplacing] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<string | null>(null)
  const [saved, setSaved] = useState<string | null>(null)

  return (
    <Card>
      <CardHeader>
        <CardTitle>DNS providers</CardTitle>
        <CardDescription>
          DNS-01 issuance sets a TXT record through a provider, which is what allows wildcard
          names and sites that are not reachable on port 80.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-6">
        {list.isPending ? (
          <p className="text-muted-foreground text-sm">Loading providers…</p>
        ) : list.error ? (
          <p role="alert" className="text-destructive text-sm">
            {errorText(list.error, "The providers could not be loaded.")}
          </p>
        ) : (
          <>
            <ProviderList
              providers={list.data.providers}
              onReplace={(name) => {
                setReplacing(name)
                setSaved(null)
              }}
              onDelete={setDeleting}
            />
            <div className="flex flex-col gap-3">
              <h3 className="text-sm font-semibold">
                {replacing ? `Replace the token for ${replacing}` : "Add a provider"}
              </h3>
              <ProviderForm
                key={replacing ?? "new"}
                kinds={list.data.kinds}
                replacing={replacing}
                pending={save.isPending}
                error={save.error}
                onCancelReplace={() => setReplacing(null)}
                onSubmit={(input) =>
                  save.mutate(input, {
                    onSuccess: () => {
                      setSaved(input.name)
                      setReplacing(null)
                    },
                  })
                }
              />
              <p role="status" className="text-xs">
                {saved ? `Saved ${saved}. The token is stored and cannot be viewed again.` : ""}
              </p>
            </div>
          </>
        )}
      </CardContent>

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) {
            setDeleting(null)
            remove.reset()
          }
        }}
        title={`Delete ${deleting ?? "provider"}?`}
        confirmLabel="Delete provider"
        pendingLabel="Deleting…"
        pending={remove.isPending}
        destructive
        error={remove.error ? errorText(remove.error, "The provider could not be deleted.") : null}
        onConfirm={() => deleting && remove.mutate(deleting, { onSuccess: () => setDeleting(null) })}
      >
        <p>
          The stored token is deleted. A provider that issued a certificate cannot be deleted: the
          panel lists those domains instead, and you remove or re-issue them first.
        </p>
      </ConfirmDialog>
    </Card>
  )
}

/** Saved providers. Only the name, service and age are known: the token is never sent back. */
export function ProviderList({
  providers,
  onReplace,
  onDelete,
}: {
  providers: DNSProvider[]
  onReplace: (name: string) => void
  onDelete: (name: string) => void
}) {
  if (providers.length === 0) {
    return <p className="text-sm">No DNS provider is saved. DNS-01 needs one.</p>
  }
  return (
    <ul className="divide-border divide-y rounded-lg border" aria-label="Saved DNS providers">
      {providers.map((p) => (
        <li key={p.name} className="flex flex-wrap items-center justify-between gap-3 px-3 py-2">
          <div className="flex flex-col">
            <span className="text-sm font-medium">{p.name}</span>
            <span className="text-muted-foreground text-xs">
              {p.kind}, token saved {formatDate(p.updated_at)}
            </span>
          </div>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => onReplace(p.name)}>
              Replace token
            </Button>
            <Button variant="outline" size="sm" onClick={() => onDelete(p.name)}>
              Delete
            </Button>
          </div>
        </li>
      ))}
    </ul>
  )
}

/* --- Panel certificate -------------------------------------------------- */

function PanelCertCard() {
  const cert = usePanelCert()
  const providers = useTLSProviders()
  const upload = useUploadPanelCert()
  const issue = useIssuePanelCert()
  const refresh = useRefreshTLS()
  const [jobId, setJobId] = useState<string | null>(null)
  const [uploaded, setUploaded] = useState(false)

  return (
    <Card>
      <CardHeader>
        <CardTitle>Panel certificate</CardTitle>
        <CardDescription>
          The certificate this panel's own address is served with, separate from your sites.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-6">
        {cert.isPending ? (
          <p className="text-muted-foreground text-sm">Loading the panel certificate…</p>
        ) : cert.error ? (
          <p role="alert" className="text-destructive text-sm">
            {errorText(cert.error, "The panel certificate could not be loaded.")}
          </p>
        ) : (
          <PanelCertSummary cert={cert.data.cert} previousAvailable={cert.data.previous_available} />
        )}

        <p className="text-muted-foreground text-sm">
          A new certificate is picked up without restarting the panel: connections opened after
          the change use it.
        </p>

        <div className="flex flex-col gap-3">
          <h3 className="text-sm font-semibold">Upload a certificate</h3>
          <UploadCertForm
            pending={upload.isPending}
            error={upload.error}
            submitLabel="Upload and apply"
            onSubmit={(input) => {
              setUploaded(false)
              upload.mutate(
                {
                  certificate: input.certificate,
                  private_key: input.private_key,
                  ...(input.chain !== "" ? { chain: input.chain } : {}),
                },
                { onSuccess: () => setUploaded(true) },
              )
            }}
          />
          <p role="status" className="text-sm">
            {uploaded ? "Certificate applied." : ""}
          </p>
        </div>

        <div className="flex flex-col gap-3">
          <h3 className="text-sm font-semibold">Get one for a hostname</h3>
          {providers.isPending ? (
            <p className="text-muted-foreground text-sm">Loading providers…</p>
          ) : (
            <PanelIssueForm
              providers={providers.data?.providers ?? []}
              pending={issue.isPending}
              error={issue.error}
              onSubmit={(input) => issue.mutate(input, { onSuccess: setJobId })}
            />
          )}
        </div>
      </CardContent>

      <JobsDrawer
        jobId={jobId}
        onClose={() => {
          setJobId(null)
          refresh()
        }}
      />
    </Card>
  )
}

/** The certificate in use, without actions. A self-signed one is flagged because browsers warn about it. */
export function PanelCertSummary({
  cert,
  previousAvailable,
}: {
  cert: CertInfo
  previousAvailable: boolean
}) {
  const daysLeft = Math.floor((Date.parse(cert.not_after) - Date.now()) / 86_400_000)
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        {cert.self_signed ? (
          <Badge variant="outline" className="border-amber-500 text-amber-700 dark:text-amber-400">
            Self-signed
          </Badge>
        ) : (
          <Badge variant="secondary">Issued by a CA</Badge>
        )}
        {Number.isNaN(daysLeft) ? null : <ExpiryBadge daysLeft={daysLeft} />}
      </div>
      {cert.self_signed && (
        <p className="text-sm">
          Browsers warn about this certificate. Upload one or request one below to remove the
          warning.
        </p>
      )}
      <dl className="grid grid-cols-[max-content_1fr] items-baseline gap-x-6 gap-y-2 text-sm">
        <dt className="text-muted-foreground">Subject</dt>
        <dd className="break-all">{cert.subject}</dd>
        <dt className="text-muted-foreground">Names</dt>
        <dd className="font-mono text-xs break-all">{cert.sans.join(", ") || "none"}</dd>
        <dt className="text-muted-foreground">Issuer</dt>
        <dd className="break-all">{cert.issuer}</dd>
        <dt className="text-muted-foreground">Valid</dt>
        <dd>
          {formatDate(cert.not_before)} to {formatDate(cert.not_after)}
        </dd>
        <dt className="text-muted-foreground">Serial</dt>
        <dd className="font-mono text-xs break-all">{cert.serial}</dd>
      </dl>
      {previousAvailable && (
        <p className="text-muted-foreground text-xs">The previous certificate is still on the server.</p>
      )}
    </div>
  )
}
