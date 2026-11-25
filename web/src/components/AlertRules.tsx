import { useId, useState, type FormEvent } from "react"

import { ConfirmDialog } from "@/components/ConfirmDialog"
import { ApplyError } from "@/components/ApplyError"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
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
import { SkeletonRows } from "@/components/ui/skeleton"
import {
  describeThresholds,
  formatHold,
  kindInfo,
  useAlertRules,
  useCreateAlertRule,
  useDeleteAlertRule,
  useUpdateAlertRule,
  type AlertRule,
  type AlertRuleInput,
  type RuleSeverity,
} from "@/lib/alertRules"
import { ApiError } from "@/lib/api"
import { describeError } from "@/lib/errors"
import { useClearMetrics, useMetricsStatus } from "@/lib/metrics"
import { formatBytes } from "@/lib/sysinfo"

const SEVERITIES: RuleSeverity[] = ["info", "warning", "critical"]
const SEVERITY_VARIANT: Record<RuleSeverity, "info" | "warning" | "destructive"> = {
  info: "info",
  warning: "warning",
  critical: "destructive",
}

/** The one-sentence rule of hysteresis, in the words of the rule's own kind. */
export function hysteresisText(kind: string): string {
  const info = kindInfo(kind)
  if (info.reversed) {
    return "This value counts down (days left), so the comparison is reversed: the alert fires once it has stayed at or below the threshold for the \"for\" time, and clears once it has stayed above the clear threshold for the \"clear for\" time."
  }
  return "The alert fires once the value has stayed past the threshold for the \"for\" time, and clears once it has stayed past the clear threshold for the \"clear for\" time, so a value hovering at the line does not flap."
}

const FIELDS = [
  "kind",
  "target",
  "threshold",
  "clear_threshold",
  "for_seconds",
  "clear_for_seconds",
  "severity",
] as const
type Field = (typeof FIELDS)[number]

function parseNumber(text: string): number | null {
  if (text.trim() === "") return null
  const n = Number(text)
  return Number.isFinite(n) ? n : null
}

/** The create and edit form. A rejected field (422 with `field`) is shown under that field. */
function RuleDialog({
  rule,
  kinds,
  onClose,
}: {
  /** The rule being edited; null creates a custom one. */
  rule: AlertRule | null
  kinds: string[]
  onClose: () => void
}) {
  const id = useId()
  const create = useCreateAlertRule()
  const update = useUpdateAlertRule()
  const mutation = rule ? update : create
  const builtin = rule?.builtin === true

  const first = rule?.kind ?? kinds[0] ?? "disk_pct"
  const [kind, setKind] = useState(first)
  const [target, setTarget] = useState(rule?.target ?? "*")
  const [threshold, setThreshold] = useState(String(rule?.threshold ?? kindInfo(first).defaults.threshold))
  const [clearThreshold, setClearThreshold] = useState(
    String(rule?.clear_threshold ?? kindInfo(first).defaults.clear_threshold),
  )
  const [forSeconds, setForSeconds] = useState(String(rule?.for_seconds ?? 300))
  const [clearFor, setClearFor] = useState(String(rule?.clear_for_seconds ?? 300))
  const [severity, setSeverity] = useState<RuleSeverity>(rule?.severity ?? "warning")
  const [local, setLocal] = useState<{ field: Field; text: string } | null>(null)

  const info = kindInfo(kind)
  const hostOnly = info.targetHint === null

  const error = mutation.error
  const apiField = error instanceof ApiError && error.field ? (error.field as Field) : null
  const fieldFailed: Field | null =
    apiField !== null && (FIELDS as readonly string[]).includes(apiField) ? apiField : null
  const bad = (f: Field) => local?.field === f || fieldFailed === f
  const fieldError = (f: Field) => {
    const text = local?.field === f ? local.text : fieldFailed === f && error instanceof ApiError ? error.detail : null
    return text ? (
      <p id={`${id}-${f}-error`} role="alert" className="text-destructive text-xs">
        {text}
      </p>
    ) : null
  }
  const described = (f: Field) => (bad(f) ? `${id}-${f}-error` : undefined)

  const pickKind = (next: string) => {
    setKind(next)
    // A new rule starts from the kind's usual numbers; an edited one keeps what was typed.
    if (rule === null) {
      setThreshold(String(kindInfo(next).defaults.threshold))
      setClearThreshold(String(kindInfo(next).defaults.clear_threshold))
    }
    if (kindInfo(next).targetHint === null) setTarget("host")
    else if (target === "host") setTarget("*")
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const nums: [Field, string, string][] = [
      ["threshold", threshold, "The threshold"],
      ["clear_threshold", clearThreshold, "The clear threshold"],
      ["for_seconds", forSeconds, "The \"for\" time"],
      ["clear_for_seconds", clearFor, "The \"clear for\" time"],
    ]
    for (const [field, text, label] of nums) {
      if (parseNumber(text) === null) {
        setLocal({ field, text: `${label} must be a number.` })
        return
      }
    }
    setLocal(null)
    const next: AlertRuleInput = {
      kind,
      target: hostOnly ? "host" : target.trim() || "*",
      threshold: parseNumber(threshold) as number,
      clear_threshold: parseNumber(clearThreshold) as number,
      for_seconds: parseNumber(forSeconds) as number,
      clear_for_seconds: parseNumber(clearFor) as number,
      severity,
    }
    if (rule === null) {
      create.mutate(next, { onSuccess: onClose })
      return
    }
    // PUT merges, so send only what changed; nothing changed is nothing to send.
    const patch: AlertRuleInput = {}
    for (const f of FIELDS) {
      if (builtin && (f === "kind" || f === "target")) continue
      if (next[f] !== rule[f]) (patch as Record<string, unknown>)[f] = next[f]
    }
    if (Object.keys(patch).length === 0) {
      onClose()
      return
    }
    update.mutate({ id: rule.id, patch }, { onSuccess: onClose })
  }

  const generalError = fieldFailed === null ? error : null

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{rule ? `Edit rule: ${kindInfo(rule.kind).label}` : "Add an alert rule"}</DialogTitle>
          <DialogDescription>{hysteresisText(kind)}</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} noValidate className="flex flex-col gap-4">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-kind`}>Kind</Label>
            <Select
              id={`${id}-kind`}
              value={kind}
              disabled={rule !== null}
              onChange={(e) => pickKind(e.target.value)}
              aria-invalid={bad("kind")}
              aria-describedby={described("kind")}
            >
              {kinds.map((k) => (
                <option key={k} value={k}>
                  {kindInfo(k).label}
                </option>
              ))}
            </Select>
            <p className="text-muted-foreground text-xs">The value is the {info.unit}.</p>
            {fieldError("kind")}
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-target`}>Target</Label>
            <Input
              id={`${id}-target`}
              value={hostOnly ? "host" : target}
              onChange={(e) => setTarget(e.target.value)}
              disabled={hostOnly || builtin}
              autoComplete="off"
              spellCheck={false}
              aria-invalid={bad("target")}
              aria-describedby={described("target")}
            />
            <p className="text-muted-foreground text-xs">
              {hostOnly ? "This kind always judges the whole host." : `Use ${info.targetHint}. * covers every one.`}
            </p>
            {fieldError("target")}
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-threshold`}>{info.reversed ? "Fires at or below" : "Fires at or above"}</Label>
              <Input
                id={`${id}-threshold`}
                value={threshold}
                onChange={(e) => setThreshold(e.target.value)}
                inputMode="decimal"
                autoComplete="off"
                aria-invalid={bad("threshold")}
                aria-describedby={described("threshold")}
              />
              {fieldError("threshold")}
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-clear_threshold`}>{info.reversed ? "Clears above" : "Clears below"}</Label>
              <Input
                id={`${id}-clear_threshold`}
                value={clearThreshold}
                onChange={(e) => setClearThreshold(e.target.value)}
                inputMode="decimal"
                autoComplete="off"
                aria-invalid={bad("clear_threshold")}
                aria-describedby={described("clear_threshold")}
              />
              {fieldError("clear_threshold")}
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-for_seconds`}>For (seconds)</Label>
              <Input
                id={`${id}-for_seconds`}
                value={forSeconds}
                onChange={(e) => setForSeconds(e.target.value)}
                inputMode="numeric"
                autoComplete="off"
                aria-invalid={bad("for_seconds")}
                aria-describedby={described("for_seconds")}
              />
              {fieldError("for_seconds")}
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-clear_for_seconds`}>Clear for (seconds)</Label>
              <Input
                id={`${id}-clear_for_seconds`}
                value={clearFor}
                onChange={(e) => setClearFor(e.target.value)}
                inputMode="numeric"
                autoComplete="off"
                aria-invalid={bad("clear_for_seconds")}
                aria-describedby={described("clear_for_seconds")}
              />
              {fieldError("clear_for_seconds")}
            </div>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-severity`}>Severity</Label>
            <Select
              id={`${id}-severity`}
              value={severity}
              onChange={(e) => setSeverity(e.target.value as RuleSeverity)}
              aria-invalid={bad("severity")}
              aria-describedby={described("severity")}
            >
              {SEVERITIES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </Select>
            {fieldError("severity")}
          </div>

          <ApplyError error={generalError} fallback="The rule could not be saved." />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={mutation.isPending}>
              {mutation.isPending ? "Saving…" : rule ? "Save rule" : "Add rule"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * The threshold alert rules: a table, a switch per rule, an edit dialog, and a
 * create dialog for custom rules. Built-in rules can be edited and switched off
 * but not deleted.
 */
export function AlertRules() {
  const rules = useAlertRules()
  const update = useUpdateAlertRule()
  const remove = useDeleteAlertRule()
  const [dialog, setDialog] = useState<{ rule: AlertRule | null } | null>(null)
  const [deleting, setDeleting] = useState<AlertRule | null>(null)

  const list = rules.data?.rules ?? []
  const kinds = rules.data?.kinds ?? []

  let body
  if (rules.isPending) {
    body = <SkeletonRows label="Loading alert rules…" />
  } else if (rules.error) {
    body = (
      <p role="status" className="text-muted-foreground text-sm">
        {rules.error instanceof ApiError && rules.error.status === 503
          ? "Alert rules are not available right now: the history database could not be opened."
          : describeError(rules.error, "The alert rules could not be loaded.")}
      </p>
    )
  } else {
    body = (
      <div className="overflow-x-auto rounded-md border">
        <table className="w-full text-xs">
          <caption className="sr-only">Threshold alert rules</caption>
          <thead className="bg-muted text-muted-foreground text-left">
            <tr>
              <th scope="col" className="px-3 py-1.5 font-medium">Rule</th>
              <th scope="col" className="px-3 py-1.5 font-medium">Target</th>
              <th scope="col" className="px-3 py-1.5 font-medium">Fires, clears</th>
              <th scope="col" className="px-3 py-1.5 font-medium">For, clear for</th>
              <th scope="col" className="px-3 py-1.5 font-medium">Severity</th>
              <th scope="col" className="px-3 py-1.5 font-medium">On</th>
              <th scope="col" className="px-3 py-1.5 text-right font-medium">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-border divide-y">
            {list.map((r) => {
              const name = kindInfo(r.kind).label
              const open = (r.states ?? []).length
              return (
                <tr key={r.id} data-rule={r.id}>
                  <td className="px-3 py-1.5">
                    <span className="font-medium">{name}</span>
                    {r.builtin && <span className="text-muted-foreground"> (built in)</span>}
                    {open > 0 && <span className="text-warning"> · {open} open</span>}
                  </td>
                  <td className="px-3 py-1.5 font-mono">{r.target}</td>
                  <td className="px-3 py-1.5 whitespace-nowrap">{describeThresholds(r)}</td>
                  <td className="px-3 py-1.5 whitespace-nowrap">
                    {formatHold(r.for_seconds)}, {formatHold(r.clear_for_seconds)}
                  </td>
                  <td className="px-3 py-1.5">
                    <Badge variant={SEVERITY_VARIANT[r.severity]}>{r.severity}</Badge>
                  </td>
                  <td className="px-3 py-1.5">
                    <input
                      type="checkbox"
                      role="switch"
                      className="accent-primary size-4"
                      aria-label={`${name} on ${r.target}`}
                      checked={r.enabled}
                      disabled={update.isPending && update.variables?.id === r.id}
                      onChange={(e) => update.mutate({ id: r.id, patch: { enabled: e.target.checked } })}
                    />
                  </td>
                  <td className="px-3 py-1.5">
                    <div className="flex justify-end gap-1">
                      <Button
                        size="sm"
                        variant="outline"
                        className="h-6 px-2"
                        aria-label={`Edit ${name} on ${r.target}`}
                        onClick={() => setDialog({ rule: r })}
                      >
                        Edit
                      </Button>
                      {!r.builtin && (
                        <Button
                          size="sm"
                          variant="outline"
                          className="h-6 px-2"
                          aria-label={`Delete ${name} on ${r.target}`}
                          onClick={() => {
                            remove.reset()
                            setDeleting(r)
                          }}
                        >
                          Delete
                        </Button>
                      )}
                    </div>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    )
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-3">
        <CardTitle>Alert rules</CardTitle>
        <Button size="sm" variant="outline" disabled={kinds.length === 0} onClick={() => setDialog({ rule: null })}>
          Add rule
        </Button>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <p className="text-muted-foreground text-xs">
          Each rule is judged every minute against the recorded history. {hysteresisText("disk_pct")} Built-in rules
          can be changed and switched off, not deleted.
        </p>
        {body}
        {update.error && <ApplyError error={update.error} fallback="The rule could not be changed." />}
      </CardContent>

      {dialog && <RuleDialog key={dialog.rule?.id ?? "new"} rule={dialog.rule} kinds={kinds} onClose={() => setDialog(null)} />}

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) {
            setDeleting(null)
            remove.reset()
          }
        }}
        title={deleting ? `Delete the ${kindInfo(deleting.kind).label} rule on ${deleting.target}?` : ""}
        confirmLabel="Delete rule"
        pendingLabel="Deleting…"
        pending={remove.isPending}
        destructive
        error={remove.error ? describeError(remove.error, "The rule could not be deleted.") : null}
        onConfirm={() => deleting && remove.mutate(deleting.id, { onSuccess: () => setDeleting(null) })}
      >
        <p>If it is firing, the alert resolves first.</p>
      </ConfirmDialog>
    </Card>
  )
}

/** The stored history's size, and the button that deletes it. Alert rules are kept. */
export function ClearHistory() {
  const status = useMetricsStatus()
  const clear = useClearMetrics()
  const [open, setOpen] = useState(false)

  const s = status.data
  const rows = s ? s.tiers.reduce((n, t) => n + t.rows, 0) : 0
  const unavailable = status.error instanceof ApiError && status.error.status === 503

  return (
    <Card>
      <CardHeader>
        <CardTitle>Stored history</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-muted-foreground text-sm" role="status">
          {s
            ? `${formatBytes(s.file_bytes + s.wal_bytes)} on disk, ${rows.toLocaleString()} ${rows === 1 ? "row" : "rows"}.${s.enabled ? "" : " Recording is off."}`
            : unavailable
              ? "The history database is not available."
              : status.error
                ? "The size of the history could not be read."
                : "Reading the size of the history…"}
        </p>
        <Button
          variant="destructive"
          disabled={unavailable || s === undefined}
          onClick={() => {
            clear.reset()
            setOpen(true)
          }}
        >
          Clear history
        </Button>
      </CardContent>
      <ConfirmDialog
        open={open}
        onOpenChange={(next) => {
          setOpen(next)
          if (!next) clear.reset()
        }}
        title="Delete all stored history?"
        confirmLabel="Delete history"
        pendingLabel="Deleting…"
        pending={clear.isPending}
        destructive
        error={clear.error ? describeError(clear.error, "The history could not be cleared.") : null}
        onConfirm={() => clear.mutate(undefined, { onSuccess: () => setOpen(false) })}
      >
        <p>
          This deletes every stored chart: the host, disk, service and site history. It cannot be undone, and recording
          starts again from empty.
        </p>
        <p>Alert rules are kept.</p>
      </ConfirmDialog>
    </Card>
  )
}
