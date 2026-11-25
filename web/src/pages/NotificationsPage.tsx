import { useState, type FormEvent, type ReactNode } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import {
  createNotifyChannel,
  deleteNotifyChannel,
  markNotifyRead,
  notifyChannels,
  notifyDeliveries,
  notifyEvents,
  notifyKinds,
  retryNotifyDelivery,
  testNotifyChannel,
  updateNotifyChannel,
  type Channel,
  type ChannelInput,
  type ChannelType,
  type DeliveryState,
  type Severity,
} from "@/lib/notify"

type Tab = "events" | "channels" | "deliveries"

const TABS: { id: Tab; label: string }[] = [
  { id: "events", label: "Events" },
  { id: "channels", label: "Channels" },
  { id: "deliveries", label: "Delivery log" },
]

const TYPE_LABELS: Record<ChannelType, string> = {
  smtp: "Email (SMTP)",
  webhook: "Webhook",
  telegram: "Telegram",
  discord: "Discord",
}

const INPUT = "border-input bg-card w-full rounded-md border px-2 py-1 text-sm"
const BUTTON = "border-input rounded-md border px-2 py-1 text-xs"
const PRIMARY = "bg-primary text-primary-foreground rounded-md px-3 py-1 text-sm disabled:opacity-50"

/** The Notifications page: the events list, the channels and the delivery log. */
export function NotificationsPage() {
  const [tab, setTab] = useState<Tab>("events")
  return (
    <div className="space-y-4">
      <div role="tablist" aria-label="Notifications" className="border-border flex gap-1 border-b">
        {TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={tab === t.id}
            onClick={() => setTab(t.id)}
            className={`-mb-px border-b-2 px-3 py-2 text-sm ${
              tab === t.id ? "border-primary font-medium" : "text-muted-foreground border-transparent"
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>
      {tab === "events" && <EventsTab />}
      {tab === "channels" && <ChannelsTab />}
      {tab === "deliveries" && <DeliveriesTab />}
    </div>
  )
}

function EventsTab() {
  const qc = useQueryClient()
  const [unread, setUnread] = useState(false)
  const events = useQuery({
    queryKey: ["notify", "events", "page", unread],
    queryFn: () => notifyEvents({ limit: 100, unread }),
  })
  const markAll = useMutation({
    mutationFn: () => markNotifyRead("all"),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["notify"] }),
  })

  return (
    <section aria-label="Events" className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={unread} onChange={(e) => setUnread(e.target.checked)} />
          Unread only
        </label>
        <button type="button" onClick={() => markAll.mutate()} disabled={markAll.isPending} className={BUTTON}>
          Mark all read
        </button>
      </div>
      {events.isLoading && <p className="text-muted-foreground text-sm">Loading…</p>}
      {events.isError && (
        <p role="alert" className="text-sm text-red-600">
          Could not load events.
        </p>
      )}
      {events.data?.length === 0 && <p className="text-muted-foreground text-sm">No events.</p>}
      <ul className="divide-border divide-y">
        {events.data?.map((ev) => (
          <li key={ev.id} className={`py-2 text-sm ${ev.read_at ? "" : "font-medium"}`}>
            <div className="flex flex-wrap items-center gap-2">
              <SeverityBadge severity={ev.severity} resolved={ev.resolved} />
              <span>{ev.subject}</span>
            </div>
            <div className="text-muted-foreground text-xs">
              {ev.kind} · {new Date(ev.at).toLocaleString()}
              {ev.link && (
                <>
                  {" · "}
                  <a href={ev.link} className="underline">
                    open
                  </a>
                </>
              )}
            </div>
            {ev.body && <p className="text-muted-foreground mt-1 text-xs whitespace-pre-wrap">{ev.body}</p>}
          </li>
        ))}
      </ul>
    </section>
  )
}

function SeverityBadge({ severity, resolved }: { severity: Severity; resolved: boolean }) {
  if (resolved) return <span className="rounded bg-green-100 px-1.5 text-xs text-green-800">Resolved</span>
  const cls =
    severity === "critical"
      ? "bg-red-100 text-red-800"
      : severity === "warning"
        ? "bg-amber-100 text-amber-800"
        : "bg-muted"
  return <span className={`rounded px-1.5 text-xs ${cls}`}>{severity}</span>
}

function ChannelsTab() {
  const qc = useQueryClient()
  const channels = useQuery({ queryKey: ["notify", "channels"], queryFn: () => notifyChannels() })
  const [editing, setEditing] = useState<Channel | "new" | null>(null)
  const [result, setResult] = useState<Record<number, string>>({})

  const remove = useMutation({
    mutationFn: (id: number) => deleteNotifyChannel(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["notify"] }),
  })

  async function test(ch: Channel) {
    try {
      const r = await testNotifyChannel(ch.id)
      setResult((m) => ({ ...m, [ch.id]: r.ok ? "Sent" : `Failed: ${r.error ?? "unknown error"}` }))
    } catch (err) {
      setResult((m) => ({ ...m, [ch.id]: `Failed: ${(err as Error).message}` }))
    }
    await qc.invalidateQueries({ queryKey: ["notify", "deliveries"] })
  }

  return (
    <section aria-label="Channels" className="space-y-3">
      <div className="flex justify-end">
        <button type="button" onClick={() => setEditing("new")} className={PRIMARY}>
          New channel
        </button>
      </div>
      {channels.data?.length === 0 && <p className="text-muted-foreground text-sm">No channels yet.</p>}
      <ul className="divide-border divide-y">
        {channels.data?.map((ch) => (
          <li key={ch.id} className="flex flex-wrap items-center justify-between gap-2 py-2 text-sm">
            <div>
              <div className="font-medium">
                {ch.name} <span className="text-muted-foreground text-xs">{TYPE_LABELS[ch.type]}</span>
              </div>
              <div className="text-muted-foreground text-xs">
                {ch.enabled ? "On" : "Off"} · from {ch.min_severity}
                {ch.quiet_start && ch.quiet_end ? ` · quiet ${ch.quiet_start}–${ch.quiet_end}` : ""}
                {ch.secret_unreadable && " · credentials unreadable, re-enter them"}
              </div>
              {result[ch.id] && <div className="text-xs">{result[ch.id]}</div>}
            </div>
            <div className="flex gap-2">
              <button type="button" onClick={() => test(ch)} className={BUTTON}>
                Send test
              </button>
              <button type="button" onClick={() => setEditing(ch)} className={BUTTON}>
                Edit
              </button>
              <button
                type="button"
                onClick={() => {
                  if (window.confirm(`Delete channel ${ch.name}? Its delivery log goes too.`)) remove.mutate(ch.id)
                }}
                className={`${BUTTON} text-red-700`}
              >
                Delete
              </button>
            </div>
          </li>
        ))}
      </ul>
      {editing && <ChannelForm initial={editing === "new" ? null : editing} onDone={() => setEditing(null)} />}
    </section>
  )
}

/** The per-type fields. Secrets are write-only: an empty box keeps the stored value. */
const TYPE_FIELDS: Record<ChannelType, { config: string[]; secret: string[] }> = {
  smtp: { config: ["host", "port", "username", "from", "to"], secret: ["password"] },
  webhook: { config: ["url"], secret: ["hmac_secret"] },
  telegram: { config: ["chat_id"], secret: ["bot_token"] },
  discord: { config: [], secret: ["webhook_url"] },
}

const FIELD_LABELS: Record<string, string> = {
  host: "SMTP host",
  port: "Port",
  username: "Username",
  password: "Password",
  from: "From address",
  to: "To addresses (comma separated)",
  url: "Webhook URL",
  hmac_secret: "HMAC secret (signs each request)",
  chat_id: "Chat id",
  bot_token: "Bot token",
  webhook_url: "Discord webhook URL",
}

function ChannelForm({ initial, onDone }: { initial: Channel | null; onDone: () => void }) {
  const qc = useQueryClient()
  const kinds = useQuery({ queryKey: ["notify", "kinds"], queryFn: () => notifyKinds() })
  const [type, setType] = useState<ChannelType>(initial?.type ?? "webhook")
  const [name, setName] = useState(initial?.name ?? "")
  const [minSeverity, setMinSeverity] = useState<Severity>(initial?.min_severity ?? "warning")
  const [kindList, setKindList] = useState((initial?.kinds ?? []).join(","))
  const [quietStart, setQuietStart] = useState(initial?.quiet_start ?? "")
  const [quietEnd, setQuietEnd] = useState(initial?.quiet_end ?? "")
  const [sendResolved, setSendResolved] = useState(initial?.send_resolved ?? true)
  const [enabled, setEnabled] = useState(initial?.enabled ?? true)
  const [security, setSecurity] = useState(String(initial?.config.security ?? "starttls"))
  const [sign, setSign] = useState(Boolean(initial?.config.sign))
  const [config, setConfig] = useState<Record<string, string>>(() => {
    const out: Record<string, string> = {}
    for (const f of TYPE_FIELDS[type].config) {
      const raw = initial?.config[f]
      out[f] = Array.isArray(raw) ? raw.join(",") : String(raw ?? "")
    }
    return out
  })
  const [secret, setSecret] = useState<Record<string, string>>({})
  const [error, setError] = useState<string | null>(null)

  const save = useMutation({
    mutationFn: (input: ChannelInput) =>
      initial ? updateNotifyChannel(initial.id, input) : createNotifyChannel(input),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["notify"] })
      onDone()
    },
    onError: (err: Error) => setError(err.message),
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    setError(null)

    // Only typed secrets are sent; an empty box keeps the stored value.
    const typed: Record<string, string> = {}
    for (const f of TYPE_FIELDS[type].secret) if (secret[f]) typed[f] = secret[f]

    const cfg: Record<string, unknown> = {}
    for (const f of TYPE_FIELDS[type].config) {
      const v = config[f] ?? ""
      if (f === "port") cfg.port = Number(v) || 0
      else if (f === "to") cfg.to = v.split(",").map((s) => s.trim()).filter(Boolean)
      else cfg[f] = v
    }
    if (type === "smtp") cfg.security = security
    if (type === "webhook") cfg.sign = sign

    save.mutate({
      name,
      type,
      enabled,
      min_severity: minSeverity,
      kinds: kindList.split(",").map((s) => s.trim()).filter(Boolean),
      quiet_start: quietStart,
      quiet_end: quietEnd,
      send_resolved: sendResolved,
      config: cfg,
      ...(Object.keys(typed).length ? { secret: typed } : {}),
    })
  }

  const kindNames = (kinds.data ?? []).map((k) => k.kind).join(", ")

  return (
    <form
      aria-label={initial ? `Edit ${initial.name}` : "New channel"}
      onSubmit={submit}
      className="bg-card border-border space-y-3 rounded-md border p-4 text-sm"
    >
      <Field label="Name">
        <input required value={name} onChange={(e) => setName(e.target.value)} className={INPUT} />
      </Field>
      <Field label="Type">
        <select
          value={type}
          onChange={(e) => setType(e.target.value as ChannelType)}
          disabled={initial !== null}
          className={INPUT}
        >
          {(Object.keys(TYPE_LABELS) as ChannelType[]).map((t) => (
            <option key={t} value={t}>
              {TYPE_LABELS[t]}
            </option>
          ))}
        </select>
      </Field>
      {TYPE_FIELDS[type].config.map((f) => (
        <Field key={f} label={FIELD_LABELS[f] ?? f}>
          <input value={config[f] ?? ""} onChange={(e) => setConfig({ ...config, [f]: e.target.value })} className={INPUT} />
        </Field>
      ))}
      {type === "smtp" && (
        <Field label="Security">
          <select value={security} onChange={(e) => setSecurity(e.target.value)} className={INPUT}>
            <option value="starttls">STARTTLS</option>
            <option value="tls">Implicit TLS</option>
            <option value="none">None (loopback only)</option>
          </select>
        </Field>
      )}
      {type === "webhook" && (
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={sign} onChange={(e) => setSign(e.target.checked)} />
          Sign requests with the HMAC secret
        </label>
      )}
      {TYPE_FIELDS[type].secret.map((f) => (
        <Field key={f} label={`${FIELD_LABELS[f] ?? f}${initial?.secret_set[f] ? " (set; leave empty to keep)" : ""}`}>
          <input
            type="password"
            autoComplete="off"
            value={secret[f] ?? ""}
            onChange={(e) => setSecret({ ...secret, [f]: e.target.value })}
            className={INPUT}
          />
        </Field>
      ))}
      <Field label="Minimum severity">
        <select value={minSeverity} onChange={(e) => setMinSeverity(e.target.value as Severity)} className={INPUT}>
          <option value="info">info</option>
          <option value="warning">warning</option>
          <option value="critical">critical</option>
        </select>
      </Field>
      <Field label={`Kinds, comma separated (empty means all). Known kinds: ${kindNames}`}>
        <input value={kindList} onChange={(e) => setKindList(e.target.value)} className={INPUT} />
      </Field>
      <div className="grid grid-cols-2 gap-2">
        <Field label="Quiet from (HH:MM)">
          <input value={quietStart} onChange={(e) => setQuietStart(e.target.value)} className={INPUT} placeholder="22:00" />
        </Field>
        <Field label="Quiet to (HH:MM)">
          <input value={quietEnd} onChange={(e) => setQuietEnd(e.target.value)} className={INPUT} placeholder="07:00" />
        </Field>
      </div>
      <label className="flex items-center gap-2">
        <input type="checkbox" checked={sendResolved} onChange={(e) => setSendResolved(e.target.checked)} />
        Send when a problem clears
      </label>
      <label className="flex items-center gap-2">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        Enabled
      </label>
      {error && (
        <p role="alert" className="text-red-700">
          {error}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <button type="button" onClick={onDone} className={BUTTON}>
          Cancel
        </button>
        <button type="submit" disabled={save.isPending} className={PRIMARY}>
          Save
        </button>
      </div>
    </form>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="block space-y-1">
      <span className="text-muted-foreground text-xs">{label}</span>
      {children}
    </label>
  )
}

const STATES: DeliveryState[] = ["failed", "queued", "sent", "suppressed"]

function DeliveriesTab() {
  const qc = useQueryClient()
  const [state, setState] = useState<DeliveryState | "">("")
  const rows = useQuery({
    queryKey: ["notify", "deliveries", state],
    queryFn: () => notifyDeliveries({ state, limit: 100 }),
  })
  const retry = useMutation({
    mutationFn: (id: number) => retryNotifyDelivery(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["notify", "deliveries"] }),
  })

  return (
    <section aria-label="Delivery log" className="space-y-3">
      <label className="flex items-center gap-2 text-sm">
        State
        <select value={state} onChange={(e) => setState(e.target.value as DeliveryState | "")} className="border-input bg-card rounded-md border px-2 py-1 text-sm">
          <option value="">All</option>
          {STATES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </label>
      <div className="overflow-x-auto">
        <table className="w-full text-left text-sm">
          <thead className="text-muted-foreground text-xs">
            <tr>
              <th className="py-1">When</th>
              <th>Channel</th>
              <th>Event</th>
              <th>State</th>
              <th>Attempts</th>
              <th>Last error</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {rows.data?.map((r) => (
              <tr key={r.id} className="border-border border-t align-top">
                <td className="py-1 text-xs">{new Date(r.created_at).toLocaleString()}</td>
                <td>{r.channel_name}</td>
                <td className="text-xs">
                  {r.subject}
                  <div className="text-muted-foreground">{r.kind}</div>
                </td>
                <td>
                  {r.state}
                  {r.reason && <span className="text-muted-foreground text-xs"> ({r.reason})</span>}
                </td>
                <td>{r.attempts}</td>
                <td className="max-w-xs truncate text-xs">{r.last_error}</td>
                <td>
                  {r.state === "failed" && (
                    <button type="button" onClick={() => retry.mutate(r.id)} className={BUTTON}>
                      Retry
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {rows.data?.length === 0 && <p className="text-muted-foreground text-sm">No deliveries.</p>}
      </div>
    </section>
  )
}
