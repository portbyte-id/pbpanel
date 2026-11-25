/**
 * The notification client: the events list behind the bell, the channels and
 * the delivery log (docs/design/notifications-plan.md §4.5). Every call goes
 * through the shared ApiClient, so the CSRF token and the 401 redirect apply.
 *
 * Secrets never come back from the server. A channel carries `secret_set`,
 * which names the secret fields that are stored. A write omits a field to keep
 * its stored value and sends "" to clear it.
 */

import { api, type ApiClient } from "@/lib/api"

export type Severity = "info" | "warning" | "critical"

export interface KindInfo {
  kind: string
  severity: Severity
  label: string
}

export interface NotifyEvent {
  id: number
  at: string
  kind: string
  severity: Severity
  key: string
  subject: string
  body: string
  source: string
  link: string
  fields: Record<string, string>
  resolved: boolean
  read_at: string | null
}

export interface NotifySummary {
  unread: number
  open_critical: number
  open_warning: number
}

export type ChannelType = "smtp" | "webhook" | "telegram" | "discord"

export interface Channel {
  id: number
  name: string
  type: ChannelType
  enabled: boolean
  min_severity: Severity
  kinds: string[]
  quiet_start: string
  quiet_end: string
  send_resolved: boolean
  config: Record<string, unknown>
  secret_set: Record<string, boolean>
  secret_unreadable: boolean
  created_at: string
  updated_at: string
}

export interface ChannelInput {
  name: string
  type: ChannelType
  enabled: boolean
  min_severity: Severity
  kinds: string[]
  quiet_start: string
  quiet_end: string
  send_resolved: boolean
  config: Record<string, unknown>
  /** Only the fields the operator typed. Omitted keeps the stored value; "" clears it. */
  secret?: Record<string, string>
}

export type DeliveryState = "queued" | "sent" | "failed" | "suppressed"

export interface DeliveryRow {
  id: number
  event_id: number
  channel_id: number
  channel_name: string
  kind: string
  subject: string
  state: DeliveryState
  reason: string
  attempts: number
  next_at: string | null
  last_error: string
  sent_at: string | null
  created_at: string
}

export interface TestResult {
  ok: boolean
  error?: string
}

export interface EventQuery {
  limit?: number
  before?: number
  unread?: boolean
}

export interface DeliveryQuery {
  channel?: number
  state?: DeliveryState | ""
  limit?: number
  before?: number
}

function qs(params: Record<string, string | number | undefined>): string {
  const parts: string[] = []
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "" && v !== 0) {
      parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`)
    }
  }
  return parts.length ? `?${parts.join("&")}` : ""
}

export function notifyEvents(q: EventQuery = {}, client: ApiClient = api): Promise<NotifyEvent[]> {
  return client.request<NotifyEvent[]>(
    `/api/notify/events${qs({ limit: q.limit, before: q.before, unread: q.unread ? "1" : undefined })}`,
  )
}

export function notifySummary(client: ApiClient = api): Promise<NotifySummary> {
  return client.request<NotifySummary>("/api/notify/summary")
}

export function notifyKinds(client: ApiClient = api): Promise<KindInfo[]> {
  return client.request<KindInfo[]>("/api/notify/kinds")
}

/** Marks the given events read, or every event when `ids` is "all". */
export function markNotifyRead(ids: number[] | "all", client: ApiClient = api): Promise<{ updated: number }> {
  const body = ids === "all" ? { all: true } : { ids }
  return client.request<{ updated: number }>("/api/notify/events/read", {
    method: "POST",
    body: JSON.stringify(body),
  })
}

export function notifyChannels(client: ApiClient = api): Promise<Channel[]> {
  return client.request<Channel[]>("/api/notify/channels")
}

export function createNotifyChannel(input: ChannelInput, client: ApiClient = api): Promise<Channel> {
  return client.request<Channel>("/api/notify/channels", { method: "POST", body: JSON.stringify(input) })
}

export function updateNotifyChannel(id: number, input: ChannelInput, client: ApiClient = api): Promise<Channel> {
  return client.request<Channel>(`/api/notify/channels/${id}`, { method: "PUT", body: JSON.stringify(input) })
}

export function deleteNotifyChannel(id: number, client: ApiClient = api): Promise<void> {
  return client.request<void>(`/api/notify/channels/${id}`, { method: "DELETE" })
}

/** Sends the fixed test message now and reports the outcome; it is logged as a delivery. */
export function testNotifyChannel(id: number, client: ApiClient = api): Promise<TestResult> {
  return client.request<TestResult>(`/api/notify/channels/${id}/test`, { method: "POST" })
}

export function notifyDeliveries(q: DeliveryQuery = {}, client: ApiClient = api): Promise<DeliveryRow[]> {
  return client.request<DeliveryRow[]>(
    `/api/notify/deliveries${qs({ channel: q.channel, state: q.state || undefined, limit: q.limit, before: q.before })}`,
  )
}

export function retryNotifyDelivery(id: number, client: ApiClient = api): Promise<{ queued: boolean }> {
  return client.request<{ queued: boolean }>(`/api/notify/deliveries/${id}/retry`, { method: "POST" })
}

/** The stream's event name, as the server sends it. */
export const NOTIFY_STREAM_EVENT = "notification"

/**
 * Opens the live stream and returns a function that closes it. EventSource
 * reconnects by itself; the caller refetches the list on open, so a gap while
 * disconnected is not shown as a missing event.
 */
export function openNotifyStream(
  onEvent: (ev: NotifyEvent) => void,
  onOpen?: () => void,
  makeSource: (url: string) => EventSource = (url) => new EventSource(url, { withCredentials: true }),
): () => void {
  const source = makeSource("/api/notify/stream")
  source.onopen = () => onOpen?.()
  source.addEventListener(NOTIFY_STREAM_EVENT, (e: Event) => {
    try {
      onEvent(JSON.parse((e as MessageEvent).data) as NotifyEvent)
    } catch {
      // A malformed frame is skipped; the next refetch brings the list back in line.
    }
  })
  return () => source.close()
}
