import { SkeletonRows } from "@/components/ui/skeleton"
import { Link } from "@tanstack/react-router"
import { useState, type ReactNode } from "react"

import { ConfirmDialog } from "@/components/ConfirmDialog"
import { ExpiryBadge } from "@/components/SiteTLS"
import { IssueCertForm, ProviderForm, UploadCertForm } from "@/components/TLSForms"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  TableFilter,
  TablePagination,
  TableToolbar,
  usePagedRows,
} from "@/components/ui/table-controls"
import { type CertView, type DNSProvider } from "@/lib/api"
import { describeError } from "@/lib/errors"
import { matchesQuery } from "@/lib/table"
import {
  useDeleteTLSProvider,
  useIssueTLSCert,
  useRemoveTLSCert,
  useSaveTLSProvider,
  useScanTLSProvider,
  useSites,
  useTLSCerts,
  useTLSProviders,
  useUploadTLSCert,
} from "@/lib/queries"
import { formatDate, renewalStatus, sortCertsBySoonest, sourceLabel } from "@/lib/tls"

// The panel's own certificate lives under Settings now; the summary stays
// exported from here for the tests that already import it.
export { PanelCertSummary } from "@/components/PanelCert"

/** Every certificate, the one that needs attention first; DNS providers. */
export function TLSPage() {
  return (
    <Tabs defaultValue="certificates">
      <TabsList>
        <TabsTrigger value="certificates">Certificates</TabsTrigger>
        <TabsTrigger value="providers">DNS providers</TabsTrigger>
      </TabsList>
      <TabsContent value="certificates">
        <CertificatesCard />
      </TabsContent>
      <TabsContent value="providers">
        <ProvidersCard />
      </TabsContent>
    </Tabs>
  )
}

function errorText(error: unknown, fallback: string): string {
  return describeError(error, fallback)
}

/* --- Certificates ------------------------------------------------------- */

function CertificatesCard() {
  const { data, isPending, error } = useTLSCerts()
  const sites = useSites()
  const remove = useRemoveTLSCert()
  const [removing, setRemoving] = useState<string | null>(null)
  // Only a loaded site list can tell a site's certificate from a standalone one;
  // until it loads, no certificate offers Remove.
  const siteDomains = new Set((sites.data ?? []).map((s) => s.domain))
  const sitesKnown = sites.isSuccess

  return (
    <div className="flex flex-col gap-6">
      <Card>
        <CardHeader>
          <CardTitle>Certificates</CardTitle>
          <CardDescription>
            Soonest expiry first. Open a site to renew, replace or remove its certificate. A
            certificate that no site uses can be removed here.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {isPending ? (
            <SkeletonRows label="Loading certificates…" />
          ) : error ? (
            <p role="alert" className="text-destructive text-sm">
              {errorText(error, "The certificates could not be loaded.")}
            </p>
          ) : (
            <CertificateTable
              certs={data}
              linkedDomains={siteDomains}
              renderSiteLink={(domain) => (
                <Link
                  to="/sites/$domain"
                  params={{ domain }}
                  className="font-medium underline-offset-2 hover:underline"
                >
                  {domain}
                </Link>
              )}
              renderActions={(cert) =>
                sitesKnown && !siteDomains.has(cert.domain) ? (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      remove.reset()
                      setRemoving(cert.domain)
                    }}
                  >
                    Remove
                  </Button>
                ) : null
              }
            />
          )}
        </CardContent>
      </Card>

      <AddCertificateCard />

      <ConfirmDialog
        open={removing !== null}
        onOpenChange={(open) => {
          if (!open) {
            setRemoving(null)
            remove.reset()
          }
        }}
        title={`Remove the certificate for ${removing ?? "this name"}?`}
        confirmLabel="Remove certificate"
        pendingLabel="Removing…"
        pending={remove.isPending}
        destructive
        error={remove.error ? errorText(remove.error, "The certificate could not be removed.") : null}
        onConfirm={() =>
          removing && remove.mutate(removing, { onSuccess: () => setRemoving(null) })
        }
      >
        <p>
          The stored certificate and its key are deleted. No site uses this name, so nothing is
          served with it.
        </p>
      </ConfirmDialog>
    </div>
  )
}

/**
 * Makes a certificate for a name without a site: a request (HTTP-01 is refused
 * until a site serves the name, DNS-01 needs no site) or an upload. A site that
 * later covers the name uses the stored certificate.
 */
function AddCertificateCard() {
  const [name, setName] = useState("")
  const [mode, setMode] = useState<"request" | "upload">("request")
  const [jobId, setJobId] = useState<string | null>(null)
  const [stored, setStored] = useState<string | null>(null)
  const providers = useTLSProviders()
  const issue = useIssueTLSCert()
  const upload = useUploadTLSCert()
  const domain = name.trim().toLowerCase()

  return (
    <Card>
      <CardHeader>
        <CardTitle>Add a certificate</CardTitle>
        <CardDescription>
          Make a certificate for a host name without creating a site first. A site that serves the
          name afterwards uses it.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-6">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="tls-cert-name">Host name</Label>
          <Input
            id="tls-cert-name"
            value={name}
            placeholder="shop.example.com…"
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => {
              setName(e.target.value)
              setJobId(null)
              setStored(null)
            }}
          />
        </div>

        {domain !== "" && (
          <Tabs value={mode} onValueChange={(v) => setMode(v as "request" | "upload")}>
            <TabsList>
              <TabsTrigger value="request">Request one</TabsTrigger>
              <TabsTrigger value="upload">Upload one</TabsTrigger>
            </TabsList>
            <TabsContent value="request">
              <IssueCertForm
                key={domain}
                domain={domain}
                defaultNames={[domain]}
                providers={providers.data?.providers ?? []}
                pending={issue.isPending}
                error={issue.error}
                onSubmit={(input) =>
                  issue.mutate(
                    { ...input, domain },
                    {
                      onSuccess: (id) => {
                        setStored(null)
                        setJobId(id)
                      },
                    },
                  )
                }
              />
            </TabsContent>
            <TabsContent value="upload">
              <UploadCertForm
                key={domain}
                submitLabel="Store certificate"
                pending={upload.isPending}
                error={upload.error}
                onSubmit={(input) =>
                  upload.mutate(
                    { ...input, domain },
                    {
                      onSuccess: () => {
                        setJobId(null)
                        setStored(domain)
                      },
                    },
                  )
                }
              />
            </TabsContent>
          </Tabs>
        )}

        {stored && (
          <p role="status" className="text-sm">
            Stored a certificate for {stored}. It is listed above.
          </p>
        )}
        {jobId && (
          <p role="status" className="text-sm">
            Ordering the certificate for {domain}. Follow it on the{" "}
            <Link to="/jobs" className="underline underline-offset-2">
              Jobs page
            </Link>
            ; it appears in the list above once it is stored.
          </p>
        )}
      </CardContent>
    </Card>
  )
}

const SOURCE_FILTERS = [
  { value: "http01", label: sourceLabel("http01") },
  { value: "dns01", label: sourceLabel("dns01") },
  { value: "upload", label: sourceLabel("upload") },
]

/**
 * The certificate list, soonest expiry first. A certificate a site uses shows no
 * name here: its row says it is in use, and Details opens a dialog with the site
 * and the certificate. Any other certificate shows its name and `renderActions`.
 */
export function CertificateTable({
  certs,
  linkedDomains,
  renderSiteLink,
  renderActions,
}: {
  certs: CertView[]
  /** Names that a site serves, so their certificate is in use. */
  linkedDomains: ReadonlySet<string>
  renderSiteLink: (domain: string) => ReactNode
  renderActions?: (cert: CertView) => ReactNode
}) {
  const [search, setSearch] = useState("")
  const [source, setSource] = useState("all")
  const [detail, setDetail] = useState<CertView | null>(null)
  const filtered = sortCertsBySoonest(certs).filter(
    (c) =>
      (source === "all" || c.source === source) &&
      matchesQuery([c.domain, c.issuer, c.serial, ...c.names], search),
  )
  const pager = usePagedRows(filtered, `${search}|${source}`)

  if (certs.length === 0) {
    return (
      <p className="text-sm">
        No site has a certificate yet. Open a site and use its TLS tab to request or upload one.
      </p>
    )
  }
  return (
    <>
      <TableToolbar
        search={search}
        onSearchChange={setSearch}
        searchLabel="Search certificates"
        placeholder="Search by name or issuer…"
      >
        <TableFilter label="Source" value={source} onChange={setSource} options={SOURCE_FILTERS} />
      </TableToolbar>
      {filtered.length === 0 ? (
        <p className="text-sm">No certificates match the filters.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead className="text-muted-foreground text-xs">
              <tr>
                <th scope="col" className="py-2 pr-4 font-medium">Name</th>
                <th scope="col" className="py-2 pr-4 font-medium">Expires</th>
                <th scope="col" className="py-2 pr-4 font-medium">Source</th>
                <th scope="col" className="py-2 pr-4 font-medium">Renewal</th>
                <th scope="col" className="py-2 font-medium">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {pager.rows.map((c) => {
                const linked = linkedDomains.has(c.domain)
                return (
                  <tr key={c.domain} data-domain={c.domain}>
                    <td className="py-2 pr-4">
                      {linked ? (
                        <span className="text-muted-foreground">In use by a site</span>
                      ) : (
                        <span className="font-medium">{c.domain}</span>
                      )}
                    </td>
                    <td className="py-2 pr-4">
                      <div className="flex flex-wrap items-center gap-2">
                        <span>{formatDate(c.not_after)}</span>
                        <ExpiryBadge daysLeft={c.days_left} />
                      </div>
                    </td>
                    <td className="py-2 pr-4">
                      <Badge variant="secondary">{sourceLabel(c.source)}</Badge>
                    </td>
                    <td className="py-2 pr-4">
                      <span
                        className={c.last_renewal_result === "failed" ? "text-destructive" : undefined}
                      >
                        {renewalStatus(c)}
                      </span>
                      {c.last_renewal_result === "failed" && c.last_renewal_error && (
                        <span className="text-muted-foreground block text-xs break-words">
                          {c.last_renewal_error}
                        </span>
                      )}
                    </td>
                    <td className="py-2 text-right">
                      {linked ? (
                        <Button variant="outline" size="sm" onClick={() => setDetail(c)}>
                          Details
                        </Button>
                      ) : (
                        renderActions?.(c)
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      <TablePagination pager={pager} />
      <CertificateDialog
        cert={detail}
        siteLink={detail ? renderSiteLink(detail.domain) : null}
        onClose={() => setDetail(null)}
      />
    </>
  )
}

/** The site that serves a certificate, and everything the list leaves out about it. */
function CertificateDialog({
  cert,
  siteLink,
  onClose,
}: {
  cert: CertView | null
  siteLink: ReactNode
  onClose: () => void
}) {
  return (
    <Dialog
      open={cert !== null}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent>
        {cert && (
          <>
            <DialogHeader>
              <DialogTitle>Certificate in use by a site</DialogTitle>
              <DialogDescription>The site that serves this certificate, and its details.</DialogDescription>
            </DialogHeader>
            <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-2 text-sm">
              <dt className="text-muted-foreground">Site</dt>
              <dd>{siteLink}</dd>
              <dt className="text-muted-foreground">Names</dt>
              <dd className="break-words">{cert.names.join(", ") || "—"}</dd>
              <dt className="text-muted-foreground">Expires</dt>
              <dd className="flex flex-wrap items-center gap-2">
                <span>{formatDate(cert.not_after)}</span>
                <ExpiryBadge daysLeft={cert.days_left} />
              </dd>
              <dt className="text-muted-foreground">Valid from</dt>
              <dd>{formatDate(cert.not_before)}</dd>
              <dt className="text-muted-foreground">Issuer</dt>
              <dd className="break-words">{cert.issuer || "—"}</dd>
              <dt className="text-muted-foreground">Serial</dt>
              <dd className="font-mono text-xs break-all">{cert.serial || "—"}</dd>
              <dt className="text-muted-foreground">Source</dt>
              <dd>
                {sourceLabel(cert.source)}
                {cert.provider ? `, ${cert.provider}` : ""}
              </dd>
              <dt className="text-muted-foreground">Renewal</dt>
              <dd>
                <span className={cert.last_renewal_result === "failed" ? "text-destructive" : undefined}>
                  {renewalStatus(cert)}
                </span>
                {cert.last_renewal_result === "failed" && cert.last_renewal_error && (
                  <span className="text-muted-foreground block text-xs break-words">
                    {cert.last_renewal_error}
                  </span>
                )}
              </dd>
              <dt className="text-muted-foreground">Last renewal</dt>
              <dd>{cert.last_renewal_at ? formatDate(cert.last_renewal_at) : "Never"}</dd>
              <dt className="text-muted-foreground">HTTP redirect</dt>
              <dd>{cert.redirect_http ? "Redirects to HTTPS" : "Off"}</dd>
            </dl>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

/* --- DNS providers ------------------------------------------------------ */

function ProvidersCard() {
  const list = useTLSProviders()
  const save = useSaveTLSProvider()
  const remove = useDeleteTLSProvider()
  const scan = useScanTLSProvider()
  const [replacing, setReplacing] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<string | null>(null)
  const [saved, setSaved] = useState<string | null>(null)
  // True once a scan job was started, by linking or by the button.
  const [scanStarted, setScanStarted] = useState(false)

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
          <SkeletonRows label="Loading providers…" />
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
              scanning={scan.isPending ? (scan.variables ?? null) : null}
              onScan={(name) =>
                scan.mutate(name, { onSuccess: () => setScanStarted(true) })
              }
            />
            {scan.error && (
              <p role="alert" className="text-destructive text-sm">
                {errorText(scan.error, "The domains could not be scanned.")}
              </p>
            )}
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
                    onSuccess: (res) => {
                      setSaved(input.name)
                      setReplacing(null)
                      setScanStarted(Boolean(res?.job_id))
                    },
                  })
                }
              />
              <p role="status" className="text-xs">
                {saved ? `Saved ${saved}. The token is stored and cannot be viewed again.` : ""}
              </p>
            </div>
            {scanStarted && (
              <p role="status" className="text-sm">
                Scanning the domains in this account and ordering a wildcard certificate for each.
                Follow it on the{" "}
                <Link to="/jobs" className="underline underline-offset-2">
                  Jobs page
                </Link>
                ; finished certificates appear under Certificates.
              </p>
            )}
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
  onScan,
  scanning,
}: {
  providers: DNSProvider[]
  onReplace: (name: string) => void
  onDelete: (name: string) => void
  /** Scan the provider's domains and issue a wildcard certificate for each. */
  onScan?: (name: string) => void
  scanning?: string | null
}) {
  if (providers.length === 0) {
    return <p className="text-sm">No DNS provider is saved. DNS-01 needs one.</p>
  }
  return (
    <ul className="divide-border divide-y rounded-lg border" aria-label="Saved DNS providers">
      {providers.map((p) => (
        <li key={p.name} className="flex flex-wrap items-center justify-between gap-3 px-3 py-1.5">
          <div className="flex flex-col">
            <span className="text-sm font-medium">{p.name}</span>
            <span className="text-muted-foreground text-xs">
              {p.kind}, token saved {formatDate(p.updated_at)}
            </span>
          </div>
          <div className="flex gap-2">
            {onScan && p.kind === "cloudflare" && (
              <Button
                variant="outline"
                size="sm"
                disabled={scanning === p.name}
                onClick={() => onScan(p.name)}
              >
                {scanning === p.name ? "Scanning…" : "Scan domains"}
              </Button>
            )}
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
