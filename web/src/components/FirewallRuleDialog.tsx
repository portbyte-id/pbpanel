import { useId, useState } from "react"

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
import {
  ApiError,
  type FirewallProtocol,
  type FirewallRule,
  type FirewallRuleInput,
} from "@/lib/api"
import { useSaveFirewallRule } from "@/lib/queries"

/** "80" or "8000-8100"; an empty end means a single port. */
export function parsePortSpec(text: string): { start: number; end: number } | null {
  const m = /^\s*(\d{1,5})\s*(?:-\s*(\d{1,5}))?\s*$/.exec(text)
  if (!m) return null
  const start = Number(m[1])
  const end = m[2] === undefined ? start : Number(m[2])
  return { start, end }
}

export function portLabel(rule: Pick<FirewallRule, "portStart" | "portEnd">): string {
  return rule.portStart === rule.portEnd ? String(rule.portStart) : `${rule.portStart}-${rule.portEnd}`
}

/**
 * The rule form, without the dialog or the request around it. A rejected field
 * (`invalid_rule` with `field`) is shown under that field; anything else once
 * under the form.
 */
export function FirewallRuleForm({
  rule,
  onSubmit,
  onCancel,
  pending,
  error,
}: {
  rule: FirewallRule | null
  onSubmit: (input: FirewallRuleInput) => void
  onCancel: () => void
  pending: boolean
  error: unknown
}) {
  const id = useId()
  const [ports, setPorts] = useState(rule ? portLabel(rule) : "")
  const [protocol, setProtocol] = useState<FirewallProtocol>(rule?.protocol ?? "tcp")
  const [source, setSource] = useState(rule?.source ?? "")
  const [comment, setComment] = useState(rule?.comment ?? "")
  const [enabled, setEnabled] = useState(rule?.enabled ?? true)
  const [localError, setLocalError] = useState<string | null>(null)

  const apiError = error instanceof ApiError ? error : null
  const field = apiError?.code === "invalid_rule" ? apiError.field : null
  const fieldError = (...names: string[]) =>
    apiError && field !== null && names.includes(field) ? (
      <p id={`${id}-${names[0]}-error`} role="alert" className="text-destructive text-xs">
        {apiError.detail}
      </p>
    ) : null
  const invalid = (...names: string[]) => field !== null && names.includes(field)
  const describedBy = (...names: string[]) =>
    invalid(...names) ? `${id}-${names[0]}-error` : undefined

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    const parsed = parsePortSpec(ports)
    if (!parsed) {
      setLocalError("Enter a port such as 443, or a range such as 8000-8100.")
      return
    }
    setLocalError(null)
    const input: FirewallRuleInput = {
      portStart: parsed.start,
      portEnd: parsed.end,
      protocol,
      enabled,
    }
    if (source.trim() !== "") input.source = source.trim()
    if (comment.trim() !== "") input.comment = comment.trim()
    onSubmit(input)
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-ports`}>Port or range</Label>
        <Input
          id={`${id}-ports`}
          value={ports}
          onChange={(e) => setPorts(e.target.value)}
          placeholder="443 or 8000-8100"
          inputMode="numeric"
          autoComplete="off"
          spellCheck={false}
          required
          aria-invalid={localError !== null || invalid("portStart", "portEnd")}
          aria-describedby={
            localError !== null ? `${id}-ports-local` : describedBy("portStart", "portEnd")
          }
        />
        {localError && (
          <p id={`${id}-ports-local`} role="alert" className="text-destructive text-xs">
            {localError}
          </p>
        )}
        {fieldError("portStart", "portEnd")}
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-protocol`}>Protocol</Label>
        <Select
          id={`${id}-protocol`}
          value={protocol}
          onChange={(e) => setProtocol(e.target.value as FirewallProtocol)}
          aria-invalid={invalid("protocol")}
          aria-describedby={describedBy("protocol")}
        >
          <option value="tcp">TCP</option>
          <option value="udp">UDP</option>
          <option value="both">TCP and UDP</option>
        </Select>
        {fieldError("protocol")}
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-source`}>Allowed from</Label>
        <Input
          id={`${id}-source`}
          value={source}
          onChange={(e) => setSource(e.target.value)}
          placeholder="203.0.113.0/24"
          autoComplete="off"
          spellCheck={false}
          aria-invalid={invalid("source")}
          aria-describedby={[describedBy("source"), `${id}-source-hint`].filter(Boolean).join(" ")}
        />
        <p id={`${id}-source-hint`} className="text-muted-foreground text-xs">
          Optional. An address or CIDR block. Leave empty to allow everyone.
        </p>
        {fieldError("source")}
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-comment`}>Comment</Label>
        <Input
          id={`${id}-comment`}
          value={comment}
          onChange={(e) => setComment(e.target.value)}
          autoComplete="off"
          aria-invalid={invalid("comment")}
          aria-describedby={describedBy("comment")}
        />
        {fieldError("comment")}
      </div>

      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        Rule is enabled
      </label>

      {apiError && field === null && (
        <p role="alert" className="text-destructive text-sm">
          {apiError.detail}
        </p>
      )}

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={pending || ports.trim() === ""}>
          {pending ? "Saving…" : rule ? "Save rule" : "Add rule"}
        </Button>
      </DialogFooter>
    </form>
  )
}

/** `rule` is the rule being edited; null adds a new one. */
export function FirewallRuleDialog({
  open,
  onOpenChange,
  rule,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  rule: FirewallRule | null
}) {
  const save = useSaveFirewallRule()

  const close = (next: boolean) => {
    if (!next) save.reset()
    onOpenChange(next)
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{rule ? "Edit rule" : "Add a rule"}</DialogTitle>
          <DialogDescription>
            The rule is saved now and goes live when you apply the changes.
          </DialogDescription>
        </DialogHeader>
        {open && (
          <FirewallRuleForm
            rule={rule}
            pending={save.isPending}
            error={save.error}
            onCancel={() => close(false)}
            onSubmit={(input) =>
              save.mutate({ id: rule?.id, rule: input }, { onSuccess: () => close(false) })
            }
          />
        )}
      </DialogContent>
    </Dialog>
  )
}
