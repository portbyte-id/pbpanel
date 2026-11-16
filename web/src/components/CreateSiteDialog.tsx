import { useId, useState } from "react"

import { ApplyError } from "@/components/ApplyError"
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
import { ApiError, type CreateSiteInput, type SiteKind } from "@/lib/api"
import { useCreateSite } from "@/lib/queries"

/** Aliases are typed as one line or several; commas, spaces and newlines all separate. */
export function parseAliases(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s !== "")
}

/**
 * The create form, without the dialog or the request around it, so its
 * behaviour is tested as a form.
 *
 * `error` is whatever the create returned. A rejected field is shown under that
 * field; anything else, including nginx refusing the generated config, is shown
 * once under the form with nginx's own output.
 */
export function CreateSiteForm({
  onSubmit,
  onCancel,
  pending,
  error,
}: {
  onSubmit: (input: CreateSiteInput) => void
  onCancel: () => void
  pending: boolean
  error: unknown
}) {
  const id = useId()
  const [kind, setKind] = useState<SiteKind>("static")
  const [domain, setDomain] = useState("")
  const [aliases, setAliases] = useState("")
  const [root, setRoot] = useState("")
  const [target, setTarget] = useState("")

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

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    const input: CreateSiteInput = { domain: domain.trim(), aliases: parseAliases(aliases), kind }
    if (kind === "static" && root.trim() !== "") input.root = root.trim()
    if (kind === "proxy") input.proxy_target = target.trim()
    onSubmit(input)
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-2 text-sm font-medium">What does the site serve?</legend>
        {(
          [
            ["static", "Files from a folder", "HTML, images and other static files."],
            ["proxy", "Another server", "Requests are passed to an app listening on a port."],
          ] as const
        ).map(([value, title, hint]) => (
          <label
            key={value}
            className="border-input has-checked:border-primary has-checked:bg-accent flex cursor-pointer items-start gap-3 rounded-md border p-3"
          >
            <input
              type="radio"
              name={`${id}-kind`}
              value={value}
              checked={kind === value}
              onChange={() => setKind(value)}
              className="mt-0.5"
            />
            <span className="flex flex-col gap-0.5">
              <span className="text-sm font-medium">{title}</span>
              <span className="text-muted-foreground text-xs">{hint}</span>
            </span>
          </label>
        ))}
        {fieldError("kind")}
      </fieldset>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-domain`}>Domain</Label>
        <Input
          id={`${id}-domain`}
          value={domain}
          onChange={(e) => setDomain(e.target.value)}
          placeholder="example.com"
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
          placeholder="www.example.com"
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

      {kind === "static" ? (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-root`}>Folder</Label>
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
          <p id={`${id}-root-hint`} className="text-muted-foreground text-xs">
            Leave empty to use the folder shown. It is created with a placeholder page if it does
            not exist, and must be inside /www/services/sites.
          </p>
          {fieldError("root")}
        </div>
      ) : (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-target`}>Pass requests to</Label>
          <Input
            id={`${id}-target`}
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            placeholder="http://127.0.0.1:3000"
            autoComplete="off"
            spellCheck={false}
            required
            aria-invalid={field === "proxy_target"}
            aria-describedby={describedBy("proxy_target", "hint")}
          />
          <p id={`${id}-proxy_target-hint`} className="text-muted-foreground text-xs">
            The address of the app: http or https, a host and a port, with no path.
          </p>
          {fieldError("proxy_target")}
        </div>
      )}

      {field === null && <ApplyError error={error} fallback="The site could not be created." />}

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={pending || domain.trim() === ""}>
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
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (domain: string) => void
}) {
  const create = useCreateSite()

  const close = (next: boolean) => {
    if (!next) create.reset()
    onOpenChange(next)
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add a site</DialogTitle>
          <DialogDescription>
            The panel writes the nginx configuration and checks it before nginx reloads. If nginx
            refuses it, nothing is changed.
          </DialogDescription>
        </DialogHeader>
        {/* Remounted on open, so a closed dialog does not reopen with old text. */}
        {open && (
          <CreateSiteForm
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
