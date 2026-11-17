import { useId, useState } from "react"

import { ApplyError } from "@/components/ApplyError"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select } from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import type {
  DNSProvider,
  IssueCertInput,
  IssueMethod,
  IssuePanelCertInput,
  TLSWarning,
  UploadCertInput,
} from "@/lib/api"
import { hasPEM, issueProblem, splitNames } from "@/lib/tls"

/** How the certificate is proven: the method, and for DNS-01 the provider that holds the zone. */
function MethodFields({
  id,
  method,
  onMethod,
  provider,
  onProvider,
  providers,
  email,
  onEmail,
}: {
  id: string
  method: IssueMethod
  onMethod: (m: IssueMethod) => void
  provider: string
  onProvider: (p: string) => void
  providers: DNSProvider[]
  email: string
  onEmail: (e: string) => void
}) {
  return (
    <>
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-1 text-sm font-medium">How Let's Encrypt checks the domain</legend>
        {(
          [
            ["http01", "HTTP-01", "A file is served from this server on port 80. No wildcard names."],
            ["dns01", "DNS-01", "A TXT record is set through your DNS provider. Allows wildcard names."],
          ] as const
        ).map(([value, title, hint]) => (
          <label
            key={value}
            className="border-input has-checked:border-primary has-checked:bg-accent flex cursor-pointer items-start gap-3 rounded-md border p-3"
          >
            <input
              type="radio"
              name={`${id}-method`}
              value={value}
              checked={method === value}
              onChange={() => onMethod(value)}
              className="mt-0.5"
            />
            <span className="flex flex-col gap-0.5">
              <span className="text-sm font-medium">{title}</span>
              <span className="text-muted-foreground text-xs">{hint}</span>
            </span>
          </label>
        ))}
      </fieldset>

      {method === "dns01" && (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-provider`}>DNS provider</Label>
          <Select
            id={`${id}-provider`}
            value={provider}
            onChange={(e) => onProvider(e.target.value)}
            aria-describedby={`${id}-provider-hint`}
          >
            <option value="">Choose a provider…</option>
            {providers.map((p) => (
              <option key={p.name} value={p.name}>
                {p.name} ({p.kind})
              </option>
            ))}
          </Select>
          <p id={`${id}-provider-hint`} className="text-muted-foreground text-xs">
            {providers.length === 0
              ? "No DNS provider is saved yet. Add one under TLS in the sidebar."
              : "The provider's token must be able to edit DNS records for this zone."}
          </p>
        </div>
      )}

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-email`}>Contact email</Label>
        <Input
          id={`${id}-email`}
          type="email"
          value={email}
          onChange={(e) => onEmail(e.target.value)}
          autoComplete="off"
          aria-describedby={`${id}-email-hint`}
        />
        <p id={`${id}-email-hint`} className="text-muted-foreground text-xs">
          Optional. Let's Encrypt sends expiry notices here.
        </p>
      </div>
    </>
  )
}

/** Issue a certificate for one site's names. */
export function IssueCertForm({
  domain,
  defaultNames,
  providers,
  pending,
  error,
  onSubmit,
}: {
  domain: string
  defaultNames: string[]
  providers: DNSProvider[]
  pending: boolean
  error: unknown
  onSubmit: (input: IssueCertInput) => void
}) {
  const id = useId()
  const [method, setMethod] = useState<IssueMethod>("http01")
  const [namesText, setNamesText] = useState(defaultNames.join(" "))
  const [wildcard, setWildcard] = useState(false)
  const [provider, setProvider] = useState(providers[0]?.name ?? "")
  const [email, setEmail] = useState("")
  const [problem, setProblem] = useState<string | null>(null)

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    const names = splitNames(namesText)
    if (method === "dns01" && wildcard && !names.includes(`*.${domain}`)) names.push(`*.${domain}`)
    const found = issueProblem({ method, names, provider })
    setProblem(found)
    if (found) return
    const input: IssueCertInput = { method, names }
    if (method === "dns01") input.provider = provider
    if (email.trim() !== "") input.email = email.trim()
    onSubmit(input)
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <MethodFields
        id={id}
        method={method}
        onMethod={(m) => {
          setMethod(m)
          setProblem(null)
        }}
        provider={provider}
        onProvider={setProvider}
        providers={providers}
        email={email}
        onEmail={setEmail}
      />

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-names`}>Names on the certificate</Label>
        <Input
          id={`${id}-names`}
          value={namesText}
          onChange={(e) => setNamesText(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={problem !== null}
          aria-describedby={`${id}-names-hint${problem ? ` ${id}-names-error` : ""}`}
        />
        <p id={`${id}-names-hint`} className="text-muted-foreground text-xs">
          Starts with the site's domains. Every name must already point at this server
          {method === "dns01" ? ", unless a wildcard covers it." : "."}
        </p>
        {method === "dns01" && (
          <label className="mt-1 flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={wildcard}
              onChange={(e) => setWildcard(e.target.checked)}
              className="mt-0.5"
            />
            <span>Also cover *.{domain}</span>
          </label>
        )}
        {problem && (
          <p id={`${id}-names-error`} role="alert" className="text-destructive text-xs">
            {problem}
          </p>
        )}
      </div>

      <ApplyError error={error} fallback="The certificate could not be requested." />

      <div>
        <Button type="submit" disabled={pending}>
          {pending ? "Starting…" : "Request certificate"}
        </Button>
      </div>
    </form>
  )
}

/**
 * Three boxes: certificate, chain, private key. The key is never filled from
 * anywhere and is emptied the moment the form is submitted, so a rejected upload
 * means pasting it again rather than leaving it on screen.
 */
export function UploadCertForm({
  pending,
  error,
  submitLabel = "Upload certificate",
  onSubmit,
}: {
  pending: boolean
  error: unknown
  submitLabel?: string
  onSubmit: (input: UploadCertInput) => void
}) {
  const id = useId()
  const [certificate, setCertificate] = useState("")
  const [chain, setChain] = useState("")
  const [key, setKey] = useState("")
  const [problem, setProblem] = useState<{ field: "certificate" | "key"; text: string } | null>(
    null,
  )

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!hasPEM(certificate, "CERTIFICATE")) {
      setProblem({
        field: "certificate",
        text: "This is not a PEM certificate. It should start with -----BEGIN CERTIFICATE-----.",
      })
      return
    }
    if (!hasPEM(key, "PRIVATE KEY")) {
      setProblem({
        field: "key",
        text: "This is not a PEM private key. It should start with -----BEGIN PRIVATE KEY----- or a similar line.",
      })
      return
    }
    setProblem(null)
    onSubmit({ certificate: certificate.trim(), chain: chain.trim(), private_key: key.trim() })
    setKey("")
  }

  const mono = "font-mono text-xs"
  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-cert`}>Certificate</Label>
        <Textarea
          id={`${id}-cert`}
          className={mono}
          value={certificate}
          onChange={(e) => setCertificate(e.target.value)}
          placeholder="-----BEGIN CERTIFICATE-----"
          spellCheck={false}
          autoComplete="off"
          aria-invalid={problem?.field === "certificate"}
          aria-describedby={problem?.field === "certificate" ? `${id}-cert-error` : undefined}
        />
        {problem?.field === "certificate" && (
          <p id={`${id}-cert-error`} role="alert" className="text-destructive text-xs">
            {problem.text}
          </p>
        )}
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-chain`}>Chain (optional)</Label>
        <Textarea
          id={`${id}-chain`}
          className={mono}
          value={chain}
          onChange={(e) => setChain(e.target.value)}
          placeholder="Intermediate certificates, if your CA gave you a separate file"
          spellCheck={false}
          autoComplete="off"
        />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-key`}>Private key</Label>
        <Textarea
          id={`${id}-key`}
          className={`${mono} [-webkit-text-security:disc]`}
          value={key}
          onChange={(e) => setKey(e.target.value)}
          placeholder="-----BEGIN PRIVATE KEY-----"
          spellCheck={false}
          autoComplete="off"
          data-1p-ignore
          data-lpignore="true"
          aria-invalid={problem?.field === "key"}
          aria-describedby={`${id}-key-hint${problem?.field === "key" ? ` ${id}-key-error` : ""}`}
        />
        <p id={`${id}-key-hint`} className="text-muted-foreground text-xs">
          Stored encrypted on the server and never shown again. This box is emptied when you
          upload, so paste the key again if the upload is rejected.
        </p>
        {problem?.field === "key" && (
          <p id={`${id}-key-error`} role="alert" className="text-destructive text-xs">
            {problem.text}
          </p>
        )}
      </div>

      <ApplyError error={error} fallback="The certificate could not be uploaded." />

      <div>
        <Button type="submit" disabled={pending || certificate.trim() === "" || key.trim() === ""}>
          {pending ? "Checking…" : submitLabel}
        </Button>
      </div>
    </form>
  )
}

/** Findings from an upload that went through anyway. */
export function TLSWarnings({ warnings }: { warnings: TLSWarning[] }) {
  if (warnings.length === 0) return null
  return (
    <div role="status" className="rounded-md border border-amber-500/50 bg-amber-500/5 p-3">
      <p className="text-sm font-medium">
        The certificate was saved, with{" "}
        {warnings.length === 1 ? "one warning" : `${warnings.length} warnings`}:
      </p>
      <ul className="mt-1 list-disc pl-5 text-xs" aria-label="Certificate warnings">
        {warnings.map((w) => (
          <li key={`${w.code}:${w.message}`}>{w.message}</li>
        ))}
      </ul>
    </div>
  )
}

const providerName = /^[a-z0-9][a-z0-9-]{0,31}$/

/**
 * Add a DNS provider, or replace the token of a saved one. The token field is
 * a password input that starts empty every time: the server never returns it.
 */
export function ProviderForm({
  kinds,
  replacing,
  pending,
  error,
  onSubmit,
  onCancelReplace,
}: {
  kinds: string[]
  /** The name of a saved provider whose token is being replaced. */
  replacing: string | null
  pending: boolean
  error: unknown
  onSubmit: (input: { name: string; kind: string; apiToken: string }) => void
  onCancelReplace: () => void
}) {
  const id = useId()
  const [name, setName] = useState("cloudflare")
  const [kind, setKind] = useState(kinds[0] ?? "cloudflare")
  const [token, setToken] = useState("")
  const [problem, setProblem] = useState<string | null>(null)
  const effectiveName = replacing ?? name

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!providerName.test(effectiveName.trim())) {
      setProblem("Use lowercase letters, digits and hyphens, up to 32 characters.")
      return
    }
    setProblem(null)
    onSubmit({ name: effectiveName.trim(), kind, apiToken: token.trim() })
    setToken("")
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-name`}>Name</Label>
          <Input
            id={`${id}-name`}
            value={effectiveName}
            onChange={(e) => setName(e.target.value)}
            readOnly={replacing !== null}
            autoComplete="off"
            spellCheck={false}
            aria-invalid={problem !== null}
            aria-describedby={problem ? `${id}-name-error` : undefined}
          />
          {problem && (
            <p id={`${id}-name-error`} role="alert" className="text-destructive text-xs">
              {problem}
            </p>
          )}
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-kind`}>Service</Label>
          <Select id={`${id}-kind`} value={kind} onChange={(e) => setKind(e.target.value)}>
            {kinds.map((k) => (
              <option key={k} value={k}>
                {k}
              </option>
            ))}
          </Select>
        </div>
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-token`}>API token</Label>
        <Input
          id={`${id}-token`}
          type="password"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          autoComplete="new-password"
          spellCheck={false}
          data-1p-ignore
          aria-describedby={`${id}-token-hint`}
        />
        <p id={`${id}-token-hint`} className="text-muted-foreground text-xs">
          Create a token scoped to Zone:DNS:Edit for the one zone this server needs, not a
          global API key. It is stored encrypted and is never shown again.
        </p>
      </div>

      <ApplyError error={error} fallback="The provider could not be saved." />

      <div className="flex items-center gap-3">
        <Button type="submit" disabled={pending || token.trim() === ""}>
          {pending ? "Saving…" : replacing ? `Replace token for ${replacing}` : "Save provider"}
        </Button>
        {replacing && (
          <Button type="button" variant="outline" onClick={onCancelReplace}>
            Cancel
          </Button>
        )}
      </div>
    </form>
  )
}

/** Issue a certificate for the panel's own hostname. */
export function PanelIssueForm({
  providers,
  pending,
  error,
  onSubmit,
}: {
  providers: DNSProvider[]
  pending: boolean
  error: unknown
  onSubmit: (input: IssuePanelCertInput) => void
}) {
  const id = useId()
  const [hostname, setHostname] = useState("")
  const [method, setMethod] = useState<IssueMethod>("http01")
  const [provider, setProvider] = useState(providers[0]?.name ?? "")
  const [email, setEmail] = useState("")
  const [problem, setProblem] = useState<string | null>(null)

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    const names = splitNames(hostname)
    const found =
      names.length > 1 ? "Enter one hostname." : issueProblem({ method, names, provider })
    setProblem(found)
    if (found) return
    const input: IssuePanelCertInput = { hostname: names[0], method }
    if (method === "dns01") input.provider = provider
    if (email.trim() !== "") input.email = email.trim()
    onSubmit(input)
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-host`}>Panel hostname</Label>
        <Input
          id={`${id}-host`}
          value={hostname}
          onChange={(e) => setHostname(e.target.value)}
          placeholder="panel.example.com"
          autoComplete="off"
          spellCheck={false}
          aria-invalid={problem !== null}
          aria-describedby={`${id}-host-hint${problem ? ` ${id}-host-error` : ""}`}
        />
        <p id={`${id}-host-hint`} className="text-muted-foreground text-xs">
          The name you open the panel at. It must resolve to this server.
        </p>
        {problem && (
          <p id={`${id}-host-error`} role="alert" className="text-destructive text-xs">
            {problem}
          </p>
        )}
      </div>

      <MethodFields
        id={id}
        method={method}
        onMethod={(m) => {
          setMethod(m)
          setProblem(null)
        }}
        provider={provider}
        onProvider={setProvider}
        providers={providers}
        email={email}
        onEmail={setEmail}
      />
      {method === "http01" && (
        <p className="text-muted-foreground text-xs">
          HTTP-01 is answered by the site nginx serves on port 80, so a site for this hostname
          has to exist on this server first. If there is none, use DNS-01.
        </p>
      )}

      <ApplyError error={error} fallback="The certificate could not be requested." />

      <div>
        <Button type="submit" disabled={pending}>
          {pending ? "Starting…" : "Request certificate"}
        </Button>
      </div>
    </form>
  )
}
