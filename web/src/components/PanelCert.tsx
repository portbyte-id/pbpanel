import { SkeletonRows } from "@/components/ui/skeleton"
import { useState } from "react"

import { ApplyError } from "@/components/ApplyError"
import { JobsDrawer } from "@/components/JobsDrawer"
import { ExpiryBadge } from "@/components/SiteTLS"
import { PanelIssueForm, UploadCertForm } from "@/components/TLSForms"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { type CertInfo } from "@/lib/api"
import { describeError } from "@/lib/errors"
import {
  useIssuePanelCert,
  useIssuePanelIPCert,
  usePanelCert,
  usePanelIP,
  useRefreshTLS,
  useTLSProviders,
  useUploadPanelCert,
} from "@/lib/queries"
import { formatDate } from "@/lib/tls"

function errorText(error: unknown, fallback: string): string {
  return describeError(error, fallback)
}

export function PanelCertCard() {
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
          <SkeletonRows label="Loading the panel certificate…" />
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

        <IPCertSection onJob={setJobId} />

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
            <SkeletonRows label="Loading providers…" />
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
          <Badge variant="warning">
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

/**
 * An IP-address certificate for the panel: the address the server found on its own
 * interfaces is filled in, and can be replaced, for a server whose public IP is
 * mapped to it by the provider and so never shows up on an interface.
 */
export function IPCertSection({ onJob }: { onJob: (jobId: string) => void }) {
  const detected = usePanelIP()
  const issue = useIssuePanelIPCert()
  const [typed, setTyped] = useState<string | null>(null)
  const [email, setEmail] = useState("")

  const value = typed ?? detected.data?.detected ?? ""

  return (
    <form
      className="flex flex-col gap-3"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        issue.mutate(
          { ip: value.trim(), ...(email.trim() !== "" ? { email: email.trim() } : {}) },
          { onSuccess: onJob },
        )
      }}
    >
      <h3 className="text-sm font-semibold">Get a certificate for this server's IP address</h3>
      <p className="text-muted-foreground text-sm">
        Opens the panel over https at its IP address, with no domain name. Issued by Let's
        Encrypt, valid for about six days and renewed automatically. Port 80 must be reachable
        from the internet.
      </p>
      {detected.isPending ? (
        <SkeletonRows label="Looking for this server's public IP…" />
      ) : (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="panel-ip">Public IP address</Label>
          <Input
            id="panel-ip"
            value={value}
            onChange={(e) => setTyped(e.target.value)}
            placeholder="203.0.113.10"
            autoComplete="off"
            spellCheck={false}
            aria-describedby="panel-ip-hint"
          />
          <p id="panel-ip-hint" className="text-muted-foreground text-xs">
            {detected.data?.detected
              ? `Detected ${detected.data.detected} on this server. Change it if your provider maps another public address to it.`
              : (detected.data?.reason ?? "Enter the public IP address of this server.")}
          </p>
        </div>
      )}
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="panel-ip-email">Email for expiry notices (optional)</Label>
        <Input
          id="panel-ip-email"
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          autoComplete="off"
        />
      </div>
      <ApplyError error={issue.error} fallback="The certificate could not be requested." />
      <div>
        <Button type="submit" disabled={issue.isPending || value.trim() === ""}>
          {issue.isPending ? "Starting…" : "Request IP certificate"}
        </Button>
      </div>
    </form>
  )
}
