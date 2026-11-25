/**
 * Threshold alert rules (docs/API.md "Alert rules"): the typed calls, the hooks
 * the dashboard card and the Settings editor share, and the wording for each
 * rule kind. Named apart from `lib/alerts.ts`, which derives the dashboard's
 * "needs attention" list from other endpoints.
 */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import { api, type ApiClient } from "@/lib/api"
import { pollWhileVisible } from "@/lib/wafQueries"

export type RuleSeverity = "info" | "warning" | "critical"
export type RuleStateName = "ok" | "pending" | "firing" | "clearing"

export interface RuleState {
  target: string
  state: RuleStateName
  /** Unix seconds the target entered this state. */
  since: number
  value: number | null
}

export interface AlertRule {
  id: number
  kind: string
  target: string
  threshold: number
  clear_threshold: number
  for_seconds: number
  clear_for_seconds: number
  severity: RuleSeverity
  enabled: boolean
  builtin: boolean
  /** Targets that are not quiet. The server may send null for none. */
  states: RuleState[] | null
}

export interface AlertRulesResponse {
  rules: AlertRule[]
  kinds: string[]
}

/** The writable fields. An update sends only the ones that changed. */
export type AlertRuleInput = Partial<
  Pick<
    AlertRule,
    "kind" | "target" | "threshold" | "clear_threshold" | "for_seconds" | "clear_for_seconds" | "severity" | "enabled"
  >
>

export function alertRules(client: ApiClient = api): Promise<AlertRulesResponse> {
  return client.request<AlertRulesResponse>("/api/alerts")
}

export function createAlertRule(input: AlertRuleInput, client: ApiClient = api): Promise<AlertRule> {
  return client.request<AlertRule>("/api/alerts/rules", { method: "POST", body: JSON.stringify(input) })
}

export function updateAlertRule(id: number, patch: AlertRuleInput, client: ApiClient = api): Promise<AlertRule> {
  return client.request<AlertRule>(`/api/alerts/rules/${id}`, { method: "PUT", body: JSON.stringify(patch) })
}

export function deleteAlertRule(id: number, client: ApiClient = api): Promise<void> {
  return client.request<void>(`/api/alerts/rules/${id}`, { method: "DELETE" })
}

export const alertKeys = { all: ["alerts"] as const }

export function useAlertRules() {
  return useQuery({
    queryKey: alertKeys.all,
    queryFn: () => alertRules(),
    retry: false,
    refetchInterval: pollWhileVisible(30_000),
  })
}

export function useCreateAlertRule() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Alert rule added" },
    mutationFn: (input: AlertRuleInput) => createAlertRule(input),
    onSuccess: () => client.invalidateQueries({ queryKey: alertKeys.all }),
  })
}

export function useUpdateAlertRule() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Alert rule saved" },
    mutationFn: ({ id, patch }: { id: number; patch: AlertRuleInput }) => updateAlertRule(id, patch),
    onSuccess: () => client.invalidateQueries({ queryKey: alertKeys.all }),
  })
}

export function useDeleteAlertRule() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Alert rule deleted" },
    mutationFn: (id: number) => deleteAlertRule(id),
    onSuccess: () => client.invalidateQueries({ queryKey: alertKeys.all }),
  })
}

/* --- Kinds ----------------------------------------------------------------- */

export interface KindInfo {
  label: string
  /** What the value is, for the threshold fields. */
  unit: string
  /** Short suffix printed after a number. */
  suffix: string
  /** The target the kind judges, in words; null when it is always `host`. */
  targetHint: string | null
  /** True when the value counts down, so the rule fires at or below the threshold. */
  reversed: boolean
  /** Starting values for a new rule of this kind. */
  defaults: { threshold: number; clear_threshold: number }
}

export const KIND_INFO: Record<string, KindInfo> = {
  disk_pct: {
    label: "Disk used",
    unit: "percent of the disk used",
    suffix: "%",
    targetHint: "a mount point such as /, or *",
    reversed: false,
    defaults: { threshold: 85, clear_threshold: 80 },
  },
  mem_pct: {
    label: "Memory used",
    unit: "percent of memory used",
    suffix: "%",
    targetHint: null,
    reversed: false,
    defaults: { threshold: 90, clear_threshold: 85 },
  },
  swap_pct: {
    label: "Swap used",
    unit: "percent of swap used",
    suffix: "%",
    targetHint: null,
    reversed: false,
    defaults: { threshold: 50, clear_threshold: 30 },
  },
  load_per_core: {
    label: "Load per core",
    unit: "one-minute load divided by the number of cores",
    suffix: "",
    targetHint: null,
    reversed: false,
    defaults: { threshold: 2, clear_threshold: 1.5 },
  },
  cpu_pct: {
    label: "CPU used",
    unit: "percent of all cores busy",
    suffix: "%",
    targetHint: null,
    reversed: false,
    defaults: { threshold: 95, clear_threshold: 80 },
  },
  service_down: {
    label: "Service down",
    unit: "percent of the minute the service was not running",
    suffix: "%",
    targetHint: "a service name, or *",
    reversed: false,
    defaults: { threshold: 50, clear_threshold: 10 },
  },
  cert_days: {
    label: "Certificate expiry",
    unit: "days left on the certificate",
    suffix: " d",
    targetHint: "a certificate domain, or *",
    reversed: true,
    defaults: { threshold: 14, clear_threshold: 21 },
  },
  site_5xx_pct: {
    label: "Site 5xx errors",
    unit: "percent of requests that were 5xx, judged on minutes with 50 or more requests",
    suffix: "%",
    targetHint: "a site domain, or *",
    reversed: false,
    defaults: { threshold: 5, clear_threshold: 2 },
  },
}

export function kindInfo(kind: string): KindInfo {
  return (
    KIND_INFO[kind] ?? {
      label: kind,
      unit: "value",
      suffix: "",
      targetHint: "a target, or *",
      reversed: false,
      defaults: { threshold: 0, clear_threshold: 0 },
    }
  )
}

/** "85%", "14 d", "1.5": a value with its kind's suffix. */
export function formatRuleValue(kind: string, value: number | null): string {
  if (value === null || !Number.isFinite(value)) return "–"
  const rounded = Math.abs(value) >= 100 ? Math.round(value) : Math.round(value * 100) / 100
  return `${rounded}${kindInfo(kind).suffix}`
}

/** "fires at 85% or more, clears below 80%", and the reverse for a countdown. */
export function describeThresholds(rule: Pick<AlertRule, "kind" | "threshold" | "clear_threshold">): string {
  const info = kindInfo(rule.kind)
  const fire = formatRuleValue(rule.kind, rule.threshold)
  const clear = formatRuleValue(rule.kind, rule.clear_threshold)
  return info.reversed ? `≤ ${fire}, clears above ${clear}` : `≥ ${fire}, clears below ${clear}`
}

/** "5 min", "90 s", "2 h": a hold time. */
export function formatHold(seconds: number): string {
  if (seconds === 0) return "at once"
  if (seconds % 3600 === 0) return `${seconds / 3600} h`
  if (seconds % 60 === 0) return `${seconds / 60} min`
  return `${seconds} s`
}

export const OPEN_STATES: RuleStateName[] = ["firing", "clearing", "pending"]

export interface OpenAlert {
  rule: AlertRule
  state: RuleState
}

/** Every target that is not quiet, firing first. */
export function openAlerts(rules: AlertRule[]): OpenAlert[] {
  const out: OpenAlert[] = []
  for (const rule of rules) {
    for (const state of rule.states ?? []) {
      if (OPEN_STATES.includes(state.state)) out.push({ rule, state })
    }
  }
  const rank = (a: OpenAlert) => OPEN_STATES.indexOf(a.state.state)
  return out.sort((a, b) => rank(a) - rank(b) || b.state.since - a.state.since)
}

/** How long ago, in the two largest units: "3h 5m", "12m", "40s". */
export function sinceText(sinceUnix: number, nowMs: number = Date.now()): string {
  const secs = Math.max(0, Math.floor(nowMs / 1000 - sinceUnix))
  const d = Math.floor(secs / 86_400)
  const h = Math.floor((secs % 86_400) / 3_600)
  const m = Math.floor((secs % 3_600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m`
  return `${secs}s`
}
