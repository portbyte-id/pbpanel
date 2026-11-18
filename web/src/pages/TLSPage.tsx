import { SkeletonRows } from "@/components/ui/skeleton"
import { Link } from "@tanstack/react-router"
import { useState, type ReactNode } from "react"

import { ConfirmDialog } from "@/components/ConfirmDialog"
import { ExpiryBadge } from "@/components/SiteTLS"
import { ProviderForm } from "@/components/TLSForms"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ApiError, type CertView, type DNSProvider } from "@/lib/api"
import {
  useDeleteTLSProvider,
  useSaveTLSProvider,
  useTLSCerts,
  useTLSProviders,
} from "@/lib/queries"
import { formatDate, renewalStatus, sortCertsBySoonest, sourceLabel } from "@/lib/tls"

// The panel's own certificate lives under Settings now; the summary stays
// exported from here for the tests that already import it.
export { PanelCertSummary } from "@/components/PanelCert"

/** Every site certificate, the one that needs attention first; DNS providers. */
export function TLSPage() {
  return (
    <Tabs defaultValue="certificates" className="max-w-4xl">
      <TabsList>
        <TabsTrigger value="certificates">Site certificates</TabsTrigger>
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
          <SkeletonRows label="Loading certificates…" />
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
        <li key={p.name} className="flex flex-wrap items-center justify-between gap-3 px-3 py-1.5">
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

