import { useNavigate } from "@tanstack/react-router"
import { ChevronDown, ChevronRight, Download, RefreshCw } from "lucide-react"
import { Fragment, useEffect, useId, useMemo, useState } from "react"

import { ErrorState } from "@/components/ErrorState"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select } from "@/components/ui/select"
import { SkeletonRows } from "@/components/ui/skeleton"
import { api } from "@/lib/api"
import { useSites } from "@/lib/queries"
import { toMillis, ENGINE_LABEL, engineOf, type WafAction, type WafEvent } from "@/lib/waf"
import { useWafEvents, useWafEventSummary } from "@/lib/wafQueries"

const SEVERITIES = ["CRITICAL", "ERROR", "WARNING", "NOTICE"]
const PAGE_SIZE_CHOICES = [25, 50, 100]
const DEFAULT_PAGE_SIZE = 50

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

/** The path of a request URI, without its query string. */
export function pathOf(uri: string): string {
  const q = uri.indexOf("?")
  return q < 0 ? uri : uri.slice(0, q)
}

export function actionBadge(action: WafAction) {
  return action === "blocked" ? (
    <Badge variant="destructive">Blocked</Badge>
  ) : (
    <Badge variant="warning">Detected</Badge>
  )
}

/**
 * The engine that produced an event. Absent from an older panel's events, which
 * only ModSecurity could produce, so it reads as ModSecurity.
 */
export function engineBadge(e: Pick<WafEvent, "engine">) {
  return engineOf(e) === "open-appsec" ? (
    <Badge variant="info">{ENGINE_LABEL["open-appsec"]}</Badge>
  ) : (
    <Badge variant="secondary">{ENGINE_LABEL.modsecurity}</Badge>
  )
}

/**
 * The ModSecurity audit log as a table: what was matched, for which request,
 * and whether it was blocked. A row opens to every rule that matched, and
 * "Exclude…" carries the rule to the site's Firewall settings.
 */
export function WafEvents() {
  const id = useId()
  const sites = useSites()
  const navigate = useNavigate()
  const [site, setSite] = useState("")
  const [action, setAction] = useState<WafAction | "">("")
  const [severity, setSeverity] = useState("")
  const [rule, setRule] = useState("")
  const [ip, setIp] = useState("")
  const [text, setText] = useState("")
  const [open, setOpen] = useState<number | null>(null)
  const [size, setSize] = useState(DEFAULT_PAGE_SIZE)
  const sizeId = useId()

  const dRule = useDebounced(rule.trim(), 300)
  const dIp = useDebounced(ip.trim(), 300)
  const dText = useDebounced(text.trim(), 300)

  const filters = useMemo(
    () => ({ site, action, severity, rule: dRule, ip: dIp, q: dText }),
    [site, action, severity, dRule, dIp, dText],
  )

  // The cursors of the pages above the current one, kept for the filters and size
  // they were read with: a change to either starts again from the newest page.
  const pageKey = JSON.stringify([filters, size])
  const [trail, setTrail] = useState<{ key: string; cursors: number[] }>({ key: pageKey, cursors: [] })
  const cursors = trail.key === pageKey ? trail.cursors : []
  const before = cursors.length > 0 ? cursors[cursors.length - 1] : undefined

  const events = useWafEvents(filters, before, size)
  const summary = useWafEventSummary(site, "1h")

  const rows = events.data?.events ?? []
  const total = events.data?.total ?? 0
  const hasNewer = cursors.length > 0
  const hasOlder = (events.data?.next_before ?? 0) > 0
  const firstShown = cursors.length * size + 1

  const olderPage = () => {
    if (!events.data || !hasOlder) return
    setTrail({ key: pageKey, cursors: [...cursors, events.data.next_before] })
  }
  const newerPage = () => setTrail({ key: pageKey, cursors: cursors.slice(0, -1) })

  const exclude = (domain: string, ruleId: number, uri?: string) =>
    void navigate({
      to: "/sites/$domain",
      params: { domain },
      search: { waf_rule: ruleId, ...(uri ? { waf_path: pathOf(uri) } : {}) },
    })

  // A rule from the summary belongs to the chosen site, or to the only site with events.
  const summarySite = site !== "" ? site : summary.data?.by_site.length === 1 ? summary.data.by_site[0].domain : ""

  return (
    <div className="flex flex-col gap-4">
      {summary.data && (
        <section aria-label="Last hour" className="flex flex-col gap-2">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">Last hour:</span>
            <Badge variant="destructive">{summary.data.by_action.blocked} blocked</Badge>
            <Badge variant="warning">{summary.data.by_action.detected} detected</Badge>
          </div>
          {summary.data.top_rules.length > 0 && (
            <ul aria-label="Most frequent rules" className="flex flex-wrap gap-2">
              {summary.data.top_rules.slice(0, 6).map((r) => (
                <li key={r.rule_id} className="bg-card flex items-center gap-2 rounded-md border px-2 py-1 text-xs">
                  <span className="font-mono font-medium">{r.rule_id}</span>
                  <span className="text-muted-foreground max-w-[16rem] truncate" title={r.message}>
                    {r.message}
                  </span>
                  <span className="tabular-nums">×{r.count}</span>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={summarySite === ""}
                    title={summarySite === "" ? "Choose a site first" : undefined}
                    aria-label={`Exclude rule ${r.rule_id}`}
                    onClick={() => exclude(summarySite, r.rule_id)}
                  >
                    Exclude…
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </section>
      )}

      <div className="flex flex-wrap items-end gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-site`}>Site</Label>
          <Select id={`${id}-site`} className="w-56" value={site} onChange={(e) => setSite(e.target.value)}>
            <option value="">All sites</option>
            {(sites.data ?? []).map((s) => (
              <option key={s.domain} value={s.domain}>
                {s.domain}
              </option>
            ))}
          </Select>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-action`}>Outcome</Label>
          <Select
            id={`${id}-action`}
            className="w-36"
            value={action}
            onChange={(e) => setAction(e.target.value as WafAction | "")}
          >
            <option value="">All</option>
            <option value="blocked">Blocked</option>
            <option value="detected">Detected</option>
          </Select>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-severity`}>Severity</Label>
          <Select
            id={`${id}-severity`}
            className="w-36"
            value={severity}
            onChange={(e) => setSeverity(e.target.value)}
          >
            <option value="">Any</option>
            {SEVERITIES.map((s) => (
              <option key={s} value={s}>
                {s.charAt(0) + s.slice(1).toLowerCase()}
              </option>
            ))}
          </Select>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-rule`}>Rule id</Label>
          <Input
            id={`${id}-rule`}
            className="w-28 font-mono"
            inputMode="numeric"
            value={rule}
            onChange={(e) => setRule(e.target.value)}
            autoComplete="off"
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-ip`}>Client IP</Label>
          <Input
            id={`${id}-ip`}
            className="w-40 font-mono"
            value={ip}
            onChange={(e) => setIp(e.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-q`}>Search</Label>
          <Input
            id={`${id}-q`}
            className="w-48"
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="URI or message"
            autoComplete="off"
            spellCheck={false}
          />
        </div>
        <Button type="button" variant="outline" onClick={() => void events.refetch()} disabled={events.isFetching}>
          <RefreshCw aria-hidden="true" />
          Refresh
        </Button>
        {total > 0 && (
          <Button asChild variant="outline">
            <a href={api.wafEventsAbuseIPDBUrl(filters)} download>
              <Download aria-hidden="true" />
              Export to AbuseIPDB
            </a>
          </Button>
        )}
      </div>

      {events.isPending ? (
        <SkeletonRows label="Loading WAF events…" />
      ) : events.error ? (
        <ErrorState
          error={events.error}
          fallback="The WAF audit log is not available."
          onRetry={() => void events.refetch()}
        />
      ) : rows.length === 0 ? (
        <p className="text-muted-foreground border-border rounded-md border border-dashed p-6 text-sm" role="status">
          No WAF events match. Events appear here when a site's ModSecurity mode is Detection only or Blocking.
        </p>
      ) : (
        <div className="flex flex-col gap-2">
          <div className="overflow-x-auto rounded-lg border">
            <table className="w-full text-sm">
              <caption className="sr-only">WAF audit events, newest first</caption>
              <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
                <tr>
                  <th scope="col" className="w-8 px-2 py-1.5">
                    <span className="sr-only">Details</span>
                  </th>
                  <th scope="col" className="px-3 py-1.5 font-medium">Time</th>
                  <th scope="col" className="px-3 py-1.5 font-medium">Site</th>
                  <th scope="col" className="px-3 py-1.5 font-medium">Engine</th>
                  <th scope="col" className="px-3 py-1.5 font-medium">Client</th>
                  <th scope="col" className="px-3 py-1.5 font-medium">Request</th>
                  <th scope="col" className="px-3 py-1.5 font-medium">Outcome</th>
                  <th scope="col" className="px-3 py-1.5 font-medium">Rule / attack</th>
                  <th scope="col" className="px-3 py-1.5 font-medium">Message</th>
                  <th scope="col" className="px-3 py-1.5 font-medium">Severity</th>
                </tr>
              </thead>
              <tbody className="divide-border divide-y">
                {rows.map((e) => (
                  <EventRow
                    key={e.seq}
                    event={e}
                    expanded={open === e.seq}
                    onToggle={() => setOpen(open === e.seq ? null : e.seq)}
                    onExclude={exclude}
                  />
                ))}
              </tbody>
            </table>
          </div>
          <nav aria-label="WAF event pages" className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-muted-foreground text-xs" aria-live="polite">
              Showing {firstShown}–{firstShown + rows.length - 1} of {total} events.
            </p>
            <div className="flex items-center gap-2">
              <Label htmlFor={sizeId} className="text-xs">Per page</Label>
              <Select
                id={sizeId}
                className="w-20"
                value={size}
                onChange={(e) => setSize(Number(e.target.value))}
              >
                {PAGE_SIZE_CHOICES.map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </Select>
              <Button type="button" variant="outline" size="sm" disabled={!hasNewer} onClick={newerPage}>
                Newer
              </Button>
              <Button type="button" variant="outline" size="sm" disabled={!hasOlder} onClick={olderPage}>
                Older
              </Button>
            </div>
          </nav>
        </div>
      )}
    </div>
  )
}

function EventRow({
  event: e,
  expanded,
  onToggle,
  onExclude,
}: {
  event: WafEvent
  expanded: boolean
  onToggle: () => void
  onExclude: (domain: string, ruleId: number, uri?: string) => void
}) {
  const detailId = useId()
  const engine = engineOf(e)
  const openAppsec = engine === "open-appsec"
  // open-appsec has no rule ids: a rule exclusion is a ModSecurity control, so
  // the row offers none for an event the agent produced.
  const label = openAppsec
    ? `${e.attack_type || e.message || "an attack"} on ${e.uri}`
    : `rule ${e.rule_id} on ${e.uri}`
  return (
    <Fragment>
      <tr className="hover:bg-accent/40 align-top">
        <td className="px-2 py-1.5">
          <button
            type="button"
            aria-expanded={expanded}
            aria-controls={detailId}
            aria-label={`${expanded ? "Hide" : "Show"} details of ${label}`}
            onClick={onToggle}
            className="hover:bg-accent focus-visible:ring-ring/50 rounded p-1 outline-none focus-visible:ring-[3px]"
          >
            {expanded ? (
              <ChevronDown className="size-4" aria-hidden="true" />
            ) : (
              <ChevronRight className="size-4" aria-hidden="true" />
            )}
          </button>
        </td>
        <td className="px-3 py-1.5 text-xs whitespace-nowrap tabular-nums">
          {new Date(toMillis(e.time)).toLocaleString()}
        </td>
        <td className="px-3 py-1.5 text-xs">{e.site}</td>
        <td className="px-3 py-1.5">{engineBadge(e)}</td>
        <td className="px-3 py-1.5 font-mono text-xs">{e.ip}</td>
        <td className="max-w-[20rem] px-3 py-1.5 font-mono text-xs break-all">
          <span className="text-muted-foreground">{e.method}</span> {e.uri}
        </td>
        <td className="px-3 py-1.5">{actionBadge(e.action)}</td>
        <td className="px-3 py-1.5 font-mono text-xs">
          {openAppsec ? (
            // The agent's "rule" is what it says fired; the id column would be 0.
            <span className="font-sans">{e.attack_type || "—"}</span>
          ) : (
            e.rule_id
          )}
        </td>
        <td className="max-w-[22rem] px-3 py-1.5 text-xs">{e.message}</td>
        <td className="px-3 py-1.5 text-xs">{e.severity}</td>
      </tr>
      {expanded && (
        <tr id={detailId}>
          <td />
          <td colSpan={9} className="bg-muted/30 px-3 py-3">
            <div className="flex flex-col gap-3">
              <p className="text-xs">
                <span className="text-muted-foreground">Request id </span>
                <span className="font-mono">{e.request_id || "—"}</span>
                <span className="text-muted-foreground"> · response </span>
                {e.status}
              </p>

              {/* open-appsec's own fields. They are what the agent knows that the
                  ModSecurity shape has no place for. */}
              {openAppsec && (
                <dl
                  aria-label="open-appsec findings"
                  className="grid grid-cols-1 gap-x-6 gap-y-1 text-xs sm:grid-cols-3"
                >
                  <div className="flex flex-col">
                    <dt className="text-muted-foreground">Confidence</dt>
                    <dd>{e.confidence || "—"}</dd>
                  </div>
                  <div className="flex flex-col">
                    <dt className="text-muted-foreground">Attack type</dt>
                    <dd>{e.attack_type || "—"}</dd>
                  </div>
                  <div className="flex flex-col">
                    <dt className="text-muted-foreground">Matched parameter</dt>
                    <dd className="font-mono">{e.parameter || "—"}</dd>
                  </div>
                </dl>
              )}

              <ul aria-label="Matched rules" className="flex flex-col gap-2">
                {e.matches.map((m, i) => (
                  <li key={`${m.rule_id}:${i}`} className="bg-card flex flex-col gap-1 rounded-md border p-2 text-xs">
                    <div className="flex flex-wrap items-center gap-2">
                      {openAppsec ? (
                        <span className="font-medium">{m.message}</span>
                      ) : (
                        <span className="font-mono font-medium">Rule {m.rule_id}</span>
                      )}
                      <span>{m.severity}</span>
                      {m.tags.map((t) => (
                        <Badge key={t} variant="outline">
                          {t}
                        </Badge>
                      ))}
                      {/* A rule exclusion is ModSecurity's control; the agent has
                          no rule ids to exclude, so the row offers none. */}
                      {!openAppsec && (
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          className="ml-auto"
                          aria-label={`Exclude rule ${m.rule_id} on ${e.site}`}
                          onClick={() => onExclude(e.site, m.rule_id, e.uri)}
                        >
                          Exclude this rule…
                        </Button>
                      )}
                    </div>
                    {m.data && (
                      <pre className="bg-muted overflow-x-auto rounded p-1.5 font-mono whitespace-pre-wrap break-all">
                        {m.data}
                      </pre>
                    )}
                    {m.file && (
                      <p className="text-muted-foreground font-mono">
                        {m.file}:{m.line}
                      </p>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          </td>
        </tr>
      )}
    </Fragment>
  )
}
