import { ShieldAlert, ShieldCheck, ShieldOff } from "lucide-react"
import type { ReactNode } from "react"

import { Badge } from "@/components/ui/badge"
import { CLASS_SERIES } from "@/components/TrafficChart"
import {
  compactCount,
  type TrafficTotals,
  type WafEventSummary,
  type WafOverview,
  type WafOverviewSite,
} from "@/lib/waf"

/** A share as a whole percentage, or one decimal below 10 so a small rate is not rounded to zero. */
export function percent(part: number, whole: number): string {
  if (whole <= 0 || part <= 0) return "0%"
  const p = (part / whole) * 100
  return p < 0.1 ? "<0.1%" : p < 10 ? `${p.toFixed(1)}%` : `${Math.round(p)}%`
}

/** What the firewall is doing across the sites in view. */
export interface Posture {
  total: number
  blocking: number
  detecting: number
  off: number
  rateLimited: number
  challenged: number
  exclusions: number
}

export function posture(sites: WafOverviewSite[]): Posture {
  const p: Posture = { total: sites.length, blocking: 0, detecting: 0, off: 0, rateLimited: 0, challenged: 0, exclusions: 0 }
  for (const s of sites) {
    if (s.mode === "block") p.blocking++
    else if (s.mode === "detect") p.detecting++
    else p.off++
    if (s.rate_limit) p.rateLimited++
    if (s.challenge) p.challenged++
    p.exclusions += s.exclusions
  }
  return p
}

function Panel({ title, aside, children }: { title: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <section aria-label={title} className="bg-card flex min-w-0 flex-col gap-3 rounded-lg border p-3">
      <div className="flex items-baseline justify-between gap-2">
        <h2 className="text-sm font-semibold">{title}</h2>
        {aside && <div className="text-muted-foreground text-xs">{aside}</div>}
      </div>
      {children}
    </section>
  )
}

/**
 * The firewall's state at a glance: is the engine usable, how many sites it
 * blocks, only watches, or leaves open. An unprotected site is the one thing
 * on this page that needs action, so it is the only red.
 */
export function PostureBar({ overview, site }: { overview: WafOverview; site: string }) {
  const sites = site === "" ? overview.sites : overview.sites.filter((s) => s.domain === site)
  const p = posture(sites)

  if (!overview.available) {
    return (
      <div role="status" className="border-warning/50 bg-warning/10 flex items-start gap-3 rounded-lg border p-3 text-sm">
        <ShieldOff className="text-warning mt-0.5 size-5 shrink-0" aria-hidden="true" />
        <div>
          <p className="font-medium">The firewall engine is not available</p>
          <p className="text-muted-foreground text-xs">
            {overview.reason || "ModSecurity is not installed on this web server."} Traffic is still monitored.
          </p>
        </div>
      </div>
    )
  }

  const exposed = p.off > 0
  const Icon = p.total === 0 ? ShieldOff : exposed ? ShieldAlert : ShieldCheck
  const headline =
    p.total === 0
      ? "No sites yet"
      : exposed
        ? `${p.off} of ${p.total} ${p.total === 1 ? "site is" : "sites are"} not protected`
        : p.detecting > 0
          ? "Protected, with some sites only watching"
          : "All sites are protected"

  return (
    <section
      aria-label="Protection"
      className={
        "flex flex-wrap items-center gap-x-6 gap-y-2 rounded-lg border p-3 " +
        (exposed ? "border-destructive/40 bg-destructive/5" : "bg-card")
      }
    >
      <div className="flex items-center gap-3">
        <Icon
          className={"size-6 shrink-0 " + (exposed ? "text-destructive" : p.total === 0 ? "text-muted-foreground" : "text-success")}
          aria-hidden="true"
        />
        <div>
          <p className="text-sm font-semibold">{headline}</p>
          <p className="text-muted-foreground text-xs">
            {overview.flavor === "pqc" ? "ModSecurity with OWASP CRS" : "ModSecurity"}
            {site === "" ? "" : ` · ${site}`}
          </p>
        </div>
      </div>
      <ul aria-label="Protection by feature" className="flex flex-wrap gap-2 text-xs">
        <li>
          <Badge variant={p.blocking > 0 ? "success" : "outline"}>Blocking · {p.blocking}</Badge>
        </li>
        <li>
          <Badge variant={p.detecting > 0 ? "info" : "outline"}>Detection only · {p.detecting}</Badge>
        </li>
        <li>
          <Badge variant={p.off > 0 ? "destructive" : "outline"}>Off · {p.off}</Badge>
        </li>
        <li>
          <Badge variant="outline">Rate limit · {p.rateLimited}</Badge>
        </li>
        <li>
          <Badge variant="outline">Challenge · {p.challenged}</Badge>
        </li>
        <li>
          <Badge variant="outline">Rule exclusions · {p.exclusions}</Badge>
        </li>
      </ul>
    </section>
  )
}

/** A horizontal bar made of segments, each labelled in the list beside it. */
function SplitBar({ segments, label }: { segments: { key: string; value: number; color: string }[]; label: string }) {
  const total = segments.reduce((n, s) => n + s.value, 0)
  return (
    <div role="img" aria-label={label} className="bg-muted flex h-2.5 w-full overflow-hidden rounded-full">
      {total > 0 &&
        segments.map((s) =>
          s.value > 0 ? <div key={s.key} style={{ width: `${(s.value / total) * 100}%`, background: s.color }} /> : null,
        )}
    </div>
  )
}

/** Share of responses per status class: the first thing to read when something is wrong. */
export function ResponseMix({ totals }: { totals: TrafficTotals }) {
  const other = Math.max(0, totals.requests - totals.s2xx - totals.s3xx - totals.s4xx - totals.s5xx)
  const rows = CLASS_SERIES.map((c) => ({ ...c, value: totals[c.key] }))
  return (
    <Panel title="Response mix" aside={`${totals.requests.toLocaleString("en-US")} responses`}>
      <SplitBar
        label={`Responses: ${rows.map((r) => `${r.value} ${r.label}`).join(", ")}`}
        segments={[...rows.map((r) => ({ key: r.key, value: r.value, color: r.color })), { key: "other", value: other, color: "var(--muted-foreground)" }]}
      />
      <ul className="flex flex-col gap-1.5 text-sm">
        {rows.map((r) => (
          <li key={r.key} className="flex items-center gap-2">
            <span aria-hidden="true" className="inline-block size-2.5 shrink-0 rounded-sm" style={{ background: r.color }} />
            <span className="flex-1">{r.label}</span>
            <span className="tabular-nums">{r.value.toLocaleString("en-US")}</span>
            <span className="text-muted-foreground w-12 text-right text-xs tabular-nums">{percent(r.value, totals.requests)}</span>
          </li>
        ))}
      </ul>
    </Panel>
  )
}

const SEVERITY_VARIANT: Record<string, "destructive" | "warning" | "info" | "outline"> = {
  CRITICAL: "destructive",
  ERROR: "destructive",
  WARNING: "warning",
  NOTICE: "info",
}

/**
 * What the firewall caught in the period: blocked against only detected, the
 * rules that fired most, and the sites they fired on. Needs the audit summary;
 * when that is missing the rest of the page still works, so this says so and stops.
 */
export function FirewallActivity({
  summary,
  failed,
  pending,
}: {
  summary: WafEventSummary | undefined
  failed: boolean
  pending: boolean
}) {
  if (!summary) {
    return (
      <Panel title="Firewall activity">
        <p className="text-muted-foreground text-sm" role="status">
          {pending
            ? "Loading firewall events…"
            : failed
              ? "The firewall audit is not available, so rule activity cannot be shown."
              : "No firewall events."}
        </p>
      </Panel>
    )
  }
  const { blocked, detected } = summary.by_action
  const rules = summary.top_rules.slice(0, 6)
  const peak = Math.max(1, ...rules.map((r) => r.count))
  const sites = summary.by_site.slice(0, 5)
  const sitePeak = Math.max(1, ...sites.map((s) => s.count))
  return (
    <Panel title="Firewall activity" aside="Full detail under Logs → WAF audit">
      <SplitBar
        label={`${blocked} blocked, ${detected} detected`}
        segments={[
          { key: "blocked", value: blocked, color: "var(--destructive)" },
          { key: "detected", value: detected, color: "var(--warning)" },
        ]}
      />
      <div className="flex flex-wrap gap-x-5 gap-y-1 text-sm">
        <span className="flex items-center gap-1.5">
          <span aria-hidden="true" className="bg-destructive inline-block size-2.5 rounded-sm" />
          <span className="tabular-nums font-medium">{blocked.toLocaleString("en-US")}</span> blocked
        </span>
        <span className="flex items-center gap-1.5">
          <span aria-hidden="true" className="bg-warning inline-block size-2.5 rounded-sm" />
          <span className="tabular-nums font-medium">{detected.toLocaleString("en-US")}</span> detected only
        </span>
      </div>

      {rules.length === 0 ? (
        <p className="text-muted-foreground text-sm">No rule has fired in this period.</p>
      ) : (
        <div className="flex flex-col gap-1.5">
          <h3 className="text-muted-foreground text-xs font-medium">Top triggered rules</h3>
          <ol aria-label="Top triggered rules" className="flex flex-col gap-2">
            {rules.map((r) => (
              <li key={r.rule_id} className="flex flex-col gap-1">
                <div className="flex items-center gap-2 text-xs">
                  <span className="font-mono font-medium">{r.rule_id}</span>
                  <Badge variant={SEVERITY_VARIANT[r.severity.toUpperCase()] ?? "outline"}>{r.severity || "—"}</Badge>
                  <span className="text-muted-foreground min-w-0 flex-1 truncate" title={r.message}>
                    {r.message}
                  </span>
                  <span className="tabular-nums">{r.count.toLocaleString("en-US")}</span>
                </div>
                <div aria-hidden="true" className="bg-muted h-1 rounded-full">
                  <div className="bg-primary/70 h-1 rounded-full" style={{ width: `${(r.count / peak) * 100}%` }} />
                </div>
              </li>
            ))}
          </ol>
        </div>
      )}

      {sites.length > 0 && (
        <div className="flex flex-col gap-1.5">
          <h3 className="text-muted-foreground text-xs font-medium">Most targeted sites</h3>
          <ul aria-label="Most targeted sites" className="flex flex-col gap-1.5">
            {sites.map((s) => (
              <li key={s.domain} className="flex items-center gap-2 text-xs">
                <span className="w-40 truncate" title={s.domain}>
                  {s.domain}
                </span>
                <div aria-hidden="true" className="bg-muted h-1 flex-1 rounded-full">
                  <div className="bg-destructive/60 h-1 rounded-full" style={{ width: `${(s.count / sitePeak) * 100}%` }} />
                </div>
                <span className="w-10 text-right tabular-nums">{compactCount(s.count)}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </Panel>
  )
}
