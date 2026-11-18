import { SkeletonRows } from "@/components/ui/skeleton"
import { Pencil, Plus, Trash2, TriangleAlert } from "lucide-react"
import { useEffect, useId, useState } from "react"

import { ConfirmDialog } from "@/components/ConfirmDialog"
import { FirewallRuleDialog, portLabel } from "@/components/FirewallRuleDialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { calloutVariants } from "@/components/ui/callout"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ApiError, type FirewallPending, type FirewallRule, type FirewallState } from "@/lib/api"
import { coarseSeconds, useCountdown } from "@/lib/countdown"
import {
  useAddBlocklistEntry,
  useApplyFirewall,
  useConfirmFirewall,
  useDeleteFirewallRule,
  useDisableFirewall,
  useEnableFirewall,
  useFirewall,
  useFirewallBlocklist,
  useFirewallRules,
  useRemoveBlocklistEntry,
  useRevertFirewall,
} from "@/lib/queries"

const FALLBACK_BY_CODE: Record<string, string> = {
  apply_pending: "A change is already waiting to be kept or reverted.",
  not_enabled: "The firewall is not enabled.",
  expired: "The time to keep the change ran out, so the previous rules were restored.",
}

/** The server's sentence, or one written here for a code it sends without one. */
export function firewallErrorText(error: unknown, fallback: string): string | null {
  if (!error) return null
  if (error instanceof ApiError) {
    return error.detail || FALLBACK_BY_CODE[error.code] || fallback
  }
  return fallback
}

function portList(ports: number[]): string {
  return ports.length === 0 ? "none detected" : ports.join(", ")
}

/** The firewall: status, the apply bar, rules, built-ins and the blocklist. */
export function FirewallPage() {
  const state = useFirewall()

  if (state.isPending) {
    return <SkeletonRows label="Loading firewall…" />
  }
  if (state.error || !state.data) {
    return (
      <p role="alert" className="text-destructive text-sm">
        {firewallErrorText(state.error, "The firewall could not be loaded.")}
      </p>
    )
  }

  const fw = state.data

  return (
    <div className="flex flex-col gap-6">
      <StatusHeader fw={fw} />
      {fw.pending && <PendingBar pending={fw.pending} />}
      {!fw.pending && fw.enabled && fw.dirty && <ApplyBar />}
      {fw.dockerPorts.length > 0 && <DockerWarning ports={fw.dockerPorts} />}
      <Tabs defaultValue="rules">
        <TabsList>
          <TabsTrigger value="rules">Rules</TabsTrigger>
          <TabsTrigger value="blocklist">Blocklist</TabsTrigger>
        </TabsList>
        <TabsContent value="rules">
          <RulesSection fw={fw} />
        </TabsContent>
        <TabsContent value="blocklist">
          <BlocklistSection />
        </TabsContent>
      </Tabs>
    </div>
  )
}

function StatusHeader({ fw }: { fw: FirewallState }) {
  const [enabling, setEnabling] = useState(false)
  const [disabling, setDisabling] = useState(false)
  const enable = useEnableFirewall()
  const disable = useDisableFirewall()

  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-base font-semibold">Firewall</h2>
        <Badge variant={fw.enabled ? "default" : "secondary"}>
          {fw.enabled ? "Enabled" : "Disabled"}
        </Badge>
        <Badge variant="outline">IPv6 {fw.ipv6 ? "on" : "off"}</Badge>
        {fw.enabled && fw.dirty && !fw.pending && (
          <Badge variant="warning">Unapplied changes</Badge>
        )}
      </div>

      {fw.enabled ? (
        <Button
          variant="outline"
          disabled={fw.pending !== null}
          onClick={() => setDisabling(true)}
        >
          Disable firewall
        </Button>
      ) : (
        <Button onClick={() => setEnabling(true)}>Enable firewall</Button>
      )}

      <ConfirmDialog
        open={enabling}
        onOpenChange={(next) => {
          if (!next) enable.reset()
          setEnabling(next)
        }}
        title="Enable the firewall?"
        confirmLabel="Enable firewall"
        pendingLabel="Enabling…"
        pending={enable.isPending}
        error={firewallErrorText(enable.error, "The firewall could not be enabled.")}
        onConfirm={() => enable.mutate(undefined, { onSuccess: () => setEnabling(false) })}
      >
        <p>
          ufw will be stopped and masked, so it cannot start again on its own. The panel&apos;s
          rules are then applied with a 60 second keep-or-rollback timer: if you do not press Keep
          in time, the previous rules come back.
        </p>
        <p>
          SSH {fw.sshPorts.length === 1 ? "port" : "ports"}{" "}
          <strong>{portList(fw.sshPorts)}</strong> and the panel port{" "}
          <strong>{fw.panelPort}</strong> stay open.
        </p>
      </ConfirmDialog>

      <ConfirmDialog
        open={disabling}
        onOpenChange={(next) => {
          if (!next) disable.reset()
          setDisabling(next)
        }}
        title="Disable the firewall?"
        confirmLabel="Disable firewall"
        pendingLabel="Disabling…"
        pending={disable.isPending}
        destructive
        error={firewallErrorText(disable.error, "The firewall could not be disabled.")}
        onConfirm={() => disable.mutate(undefined, { onSuccess: () => setDisabling(false) })}
      >
        <p>
          The panel&apos;s firewall chains are unhooked and flushed. Every port this server listens
          on becomes reachable unless something else filters it. Your saved rules are kept.
        </p>
      </ConfirmDialog>
    </div>
  )
}

function ApplyBar() {
  const apply = useApplyFirewall()
  const error = firewallErrorText(apply.error, "The changes could not be applied.")

  return (
    <section
      aria-label="Unapplied changes"
      className={calloutVariants({
        tone: "warning",
        className: "flex flex-wrap items-center justify-between gap-3",
      })}
    >
      <div className="flex flex-col gap-1">
        <p className="text-sm font-medium">Saved rules differ from what is live.</p>
        <p className="text-muted-foreground text-xs">
          Applying starts a 60 second timer. Press Keep once you can still reach the panel;
          otherwise the previous rules return.
        </p>
        {error && (
          <p role="alert" className="text-destructive text-xs">
            {error}
          </p>
        )}
      </div>
      <Button disabled={apply.isPending} onClick={() => apply.mutate()}>
        {apply.isPending ? "Applying…" : "Apply changes"}
      </Button>
    </section>
  )
}

/**
 * A change is live on a timer. The visible number ticks every second; the
 * live region beside it changes at a coarse step so a screen reader is not
 * interrupted once a second.
 */
function PendingBar({ pending }: { pending: FirewallPending }) {
  const { seconds, expired } = useCountdown(pending.deadline)
  const confirm = useConfirmFirewall()
  const revert = useRevertFirewall()
  const { refetch } = useFirewall()

  // Past the deadline the server rolls back on its own; poll until it says so.
  useEffect(() => {
    if (!expired) return
    void refetch()
    const id = setInterval(() => void refetch(), 2000)
    return () => clearInterval(id)
  }, [expired, refetch])

  const error =
    firewallErrorText(confirm.error, "The change could not be kept.") ??
    firewallErrorText(revert.error, "The change could not be reverted.")

  if (expired) {
    return (
      <section
        aria-label="Pending firewall change"
        className={calloutVariants({ tone: "warning" })}
      >
        <p role="status" className="text-sm font-medium">
          Time ran out. The previous rules are being restored…
        </p>
        <p className="text-muted-foreground text-xs">This page refreshes by itself.</p>
        {error && (
          <p role="alert" className="text-destructive mt-1 text-xs">
            {error}
          </p>
        )}
      </section>
    )
  }

  const busy = confirm.isPending || revert.isPending

  return (
    <section
      aria-label="Pending firewall change"
      className={calloutVariants({
        tone: "warning",
        className: "flex flex-wrap items-center justify-between gap-3",
      })}
    >
      <div className="flex flex-col gap-1">
        <p className="text-sm font-medium">
          Keep this change?{" "}
          <span className="font-mono tabular-nums" aria-hidden="true" data-testid="countdown">
            {seconds}s
          </span>
        </p>
        <p className="text-muted-foreground text-xs">
          If you can read this, the panel is still reachable. Without an answer the previous rules
          are restored when the timer ends.
        </p>
        <p className="sr-only" role="status" aria-live="polite">
          About {coarseSeconds(seconds)} seconds left to keep the change.
        </p>
        {error && (
          <p role="alert" className="text-destructive text-xs">
            {error}
          </p>
        )}
      </div>
      <div className="flex gap-2">
        <Button variant="outline" disabled={busy} onClick={() => revert.mutate()}>
          {revert.isPending ? "Reverting…" : "Revert now"}
        </Button>
        <Button disabled={busy} onClick={() => confirm.mutate(pending.token)}>
          {confirm.isPending ? "Keeping…" : "Keep"}
        </Button>
      </div>
    </section>
  )
}

function DockerWarning({ ports }: { ports: FirewallState["dockerPorts"] }) {
  return (
    <section
      aria-label="Docker published ports"
      className="border-destructive/40 bg-destructive/5 flex gap-3 rounded-lg border p-4"
    >
      <TriangleAlert className="text-destructive mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <div className="flex flex-col gap-1">
        <p className="text-sm font-medium">Docker published ports bypass these rules</p>
        <p className="text-muted-foreground text-xs">
          Docker adds its own rules ahead of the firewall, so a port a container publishes is
          reachable from outside whatever the rules below say. Bind the port to 127.0.0.1 or stop
          publishing it to close it.
        </p>
        <ul className="mt-1 text-xs">
          {ports.map((p) => (
            <li key={`${p.container}:${p.port}/${p.protocol}`} className="font-mono">
              {p.container}: {p.port}/{p.protocol}
            </li>
          ))}
        </ul>
      </div>
    </section>
  )
}

function protocolLabel(p: FirewallRule["protocol"]): string {
  return p === "both" ? "TCP+UDP" : p.toUpperCase()
}

function RulesSection({ fw }: { fw: FirewallState }) {
  const rules = useFirewallRules()
  const remove = useDeleteFirewallRule()
  const [editing, setEditing] = useState<FirewallRule | null>(null)
  const [adding, setAdding] = useState(false)
  const [deleting, setDeleting] = useState<FirewallRule | null>(null)

  const list = rules.data ?? []

  return (
    <section aria-labelledby="fw-rules" className="flex flex-col gap-3">
      <div className="flex items-center justify-between gap-3">
        <h3 id="fw-rules" className="text-sm font-semibold">
          Allow rules
        </h3>
        <Button size="sm" onClick={() => setAdding(true)}>
          <Plus aria-hidden="true" />
          Add rule
        </Button>
      </div>

      {rules.error && (
        <p role="alert" className="text-destructive text-sm">
          {firewallErrorText(rules.error, "The rules could not be loaded.")}
        </p>
      )}

      <div className="overflow-x-auto rounded-lg border">
        <table className="w-full text-sm">
          <caption className="sr-only">Firewall allow rules</caption>
          <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
            <tr>
              <th scope="col" className="px-3 py-1.5 font-medium">Port</th>
              <th scope="col" className="px-3 py-1.5 font-medium">Protocol</th>
              <th scope="col" className="px-3 py-1.5 font-medium">From</th>
              <th scope="col" className="px-3 py-1.5 font-medium">Comment</th>
              <th scope="col" className="px-3 py-1.5 font-medium">State</th>
              <th scope="col" className="px-3 py-1.5 text-right font-medium">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-border divide-y">
            {fw.builtins.map((b) => (
              <tr key={b.label} data-testid="builtin-row" className="bg-muted/20">
                <td className="px-3 py-1.5 font-medium">{b.label}</td>
                <td className="text-muted-foreground px-3 py-1.5" colSpan={3}>
                  {b.detail}
                </td>
                <td className="px-3 py-1.5">
                  <Badge variant="secondary">Built in</Badge>
                </td>
                <td className="px-3 py-1.5" />
              </tr>
            ))}
            {rules.isPending && (
              <tr>
                <td colSpan={6} className="text-muted-foreground px-3 py-3 text-sm">
                  Loading rules…
                </td>
              </tr>
            )}
            {!rules.isPending && list.length === 0 && (
              <tr>
                <td colSpan={6} className="text-muted-foreground px-3 py-3 text-sm">
                  No rules of your own yet. Only the built-in rows above are allowed.
                </td>
              </tr>
            )}
            {list.map((rule) => (
              <tr key={rule.id}>
                <td className="px-3 py-1.5 font-mono text-xs">{portLabel(rule)}</td>
                <td className="px-3 py-1.5">{protocolLabel(rule.protocol)}</td>
                <td className="px-3 py-1.5 font-mono text-xs">{rule.source || "Anywhere"}</td>
                <td className="text-muted-foreground px-3 py-1.5">{rule.comment || "—"}</td>
                <td className="px-3 py-1.5">
                  <Badge variant={rule.enabled ? "default" : "secondary"}>
                    {rule.enabled ? "Enabled" : "Off"}
                  </Badge>
                </td>
                <td className="px-3 py-1.5">
                  <div className="flex justify-end gap-1">
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label={`Edit rule for port ${portLabel(rule)}`}
                      onClick={() => setEditing(rule)}
                    >
                      <Pencil aria-hidden="true" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label={`Delete rule for port ${portLabel(rule)}`}
                      onClick={() => setDeleting(rule)}
                    >
                      <Trash2 aria-hidden="true" />
                    </Button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <FirewallRuleDialog
        open={adding || editing !== null}
        rule={editing}
        onOpenChange={(next) => {
          if (!next) {
            setAdding(false)
            setEditing(null)
          }
        }}
      />

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(next) => {
          if (!next) {
            remove.reset()
            setDeleting(null)
          }
        }}
        title="Delete this rule?"
        confirmLabel="Delete rule"
        pendingLabel="Deleting…"
        pending={remove.isPending}
        destructive
        error={firewallErrorText(remove.error, "The rule could not be deleted.")}
        onConfirm={() =>
          deleting && remove.mutate(deleting.id, { onSuccess: () => setDeleting(null) })
        }
      >
        <p>
          Port {deleting ? portLabel(deleting) : ""} is closed again the next time you apply the
          changes.
        </p>
      </ConfirmDialog>
    </section>
  )
}

function BlocklistSection() {
  const id = useId()
  const list = useFirewallBlocklist()
  const add = useAddBlocklistEntry()
  const remove = useRemoveBlocklistEntry()
  const [addr, setAddr] = useState("")
  const [comment, setComment] = useState("")

  const entries = list.data ?? []
  const addError = firewallErrorText(add.error, "The address could not be blocked.")

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    const input: { addr: string; comment?: string } = { addr: addr.trim() }
    if (comment.trim() !== "") input.comment = comment.trim()
    add.mutate(input, {
      onSuccess: () => {
        setAddr("")
        setComment("")
      },
    })
  }

  return (
    <section aria-labelledby="fw-blocklist" className="flex flex-col gap-3">
      <h3 id="fw-blocklist" className="text-sm font-semibold">
        Blocklist
      </h3>

      <form onSubmit={submit} className="flex flex-wrap items-start gap-3" noValidate>
        <div className="flex min-w-48 flex-col gap-1.5">
          <Label htmlFor={`${id}-addr`}>Address or CIDR</Label>
          <Input
            id={`${id}-addr`}
            value={addr}
            onChange={(e) => setAddr(e.target.value)}
            placeholder="198.51.100.7"
            autoComplete="off"
            spellCheck={false}
            aria-invalid={add.error !== null}
            aria-describedby={add.error ? `${id}-error` : undefined}
          />
        </div>
        <div className="flex min-w-48 flex-col gap-1.5">
          <Label htmlFor={`${id}-comment`}>Comment</Label>
          <Input
            id={`${id}-comment`}
            value={comment}
            onChange={(e) => setComment(e.target.value)}
            autoComplete="off"
          />
        </div>
        <Button type="submit" className="mt-[2rem]" disabled={add.isPending || addr.trim() === ""}>
          {add.isPending ? "Blocking…" : "Block address"}
        </Button>
      </form>
      {addError && (
        <p id={`${id}-error`} role="alert" className="text-destructive text-xs">
          {addError}
        </p>
      )}

      {list.error && (
        <p role="alert" className="text-destructive text-sm">
          {firewallErrorText(list.error, "The blocklist could not be loaded.")}
        </p>
      )}

      {!list.isPending && entries.length === 0 ? (
        <p className="text-muted-foreground text-sm">No blocked addresses.</p>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-sm">
            <caption className="sr-only">Blocked addresses</caption>
            <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
              <tr>
                <th scope="col" className="px-3 py-1.5 font-medium">Address</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Comment</th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {entries.map((entry) => (
                <tr key={entry.id}>
                  <td className="px-3 py-1.5 font-mono text-xs">{entry.addr}</td>
                  <td className="text-muted-foreground px-3 py-1.5">{entry.comment || "—"}</td>
                  <td className="px-3 py-1.5">
                    <div className="flex justify-end">
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={remove.isPending}
                        aria-label={`Unblock ${entry.addr}`}
                        onClick={() => remove.mutate(entry.id)}
                      >
                        Remove
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {remove.error && (
        <p role="alert" className="text-destructive text-xs">
          {firewallErrorText(remove.error, "The address could not be removed.")}
        </p>
      )}
    </section>
  )
}
