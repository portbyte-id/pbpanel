import { Link } from "@tanstack/react-router"
import { CircleAlert, TriangleAlert } from "lucide-react"

import { Panel } from "@/components/Panel"
import { Badge } from "@/components/ui/badge"
import {
  formatRuleValue,
  kindInfo,
  openAlerts,
  sinceText,
  useAlertRules,
  type OpenAlert,
  type RuleSeverity,
  type RuleStateName,
} from "@/lib/alertRules"

const STATE_TEXT: Record<RuleStateName, string> = {
  ok: "Quiet",
  pending: "Pending",
  firing: "Firing",
  clearing: "Clearing",
}

const SEVERITY_VARIANT: Record<RuleSeverity, "info" | "warning" | "destructive"> = {
  info: "info",
  warning: "warning",
  critical: "destructive",
}

/** Where a rule's target is looked at: its site, its certificate, or nowhere in particular. */
function TargetLink({ alert }: { alert: OpenAlert }) {
  const cls = "text-primary shrink-0 text-xs font-medium underline-offset-2 hover:underline"
  const { kind } = alert.rule
  const target = alert.state.target
  if (kind === "site_5xx_pct") {
    return (
      <Link to="/sites/$domain" params={{ domain: target }} search={{ section: "metrics" }} className={cls}>
        Open metrics
      </Link>
    )
  }
  if (kind === "cert_days") {
    return (
      <Link to="/sites/$domain" params={{ domain: target }} search={{ section: "ssl" }} className={cls}>
        Open SSL
      </Link>
    )
  }
  return null
}

/**
 * The threshold rules that are not quiet. It renders nothing when none is open,
 * or when the rules cannot be read (history off or unavailable): an empty box
 * would claim a health the page has not checked.
 */
export function OpenAlertsCard() {
  const rules = useAlertRules()
  const open = openAlerts(rules.data?.rules ?? [])
  if (open.length === 0) return null

  return (
    <Panel
      title="Threshold alerts"
      note={`${open.length}`}
      action={
        <Link
          to="/settings"
          search={{ tab: "Metrics" }}
          className="text-primary text-xs font-medium underline-offset-2 hover:underline"
        >
          Edit rules
        </Link>
      }
    >
      <ul className="divide-border divide-y">
        {open.map(({ rule, state }) => {
          const critical = rule.severity === "critical"
          return (
            <li
              key={`${rule.id}:${state.target}`}
              data-severity={rule.severity}
              data-state={state.state}
              className="flex items-start justify-between gap-3 px-3 py-2"
            >
              <div className="flex min-w-0 gap-2">
                {critical ? (
                  <CircleAlert className="text-destructive mt-0.5 size-4 shrink-0" aria-hidden="true" />
                ) : (
                  <TriangleAlert className="text-warning mt-0.5 size-4 shrink-0" aria-hidden="true" />
                )}
                <div className="flex min-w-0 flex-col gap-0.5">
                  <p className="text-sm font-medium break-words">
                    {kindInfo(rule.kind).label}: {state.target}
                  </p>
                  <p className="text-muted-foreground text-xs">
                    {STATE_TEXT[state.state]}, now {formatRuleValue(rule.kind, state.value)}, since{" "}
                    <span title={new Date(state.since * 1000).toLocaleString()}>{sinceText(state.since)} ago</span>
                  </p>
                </div>
              </div>
              <div className="flex shrink-0 flex-col items-end gap-1">
                <Badge variant={SEVERITY_VARIANT[rule.severity]}>{rule.severity}</Badge>
                <TargetLink alert={{ rule, state }} />
              </div>
            </li>
          )
        })}
      </ul>
    </Panel>
  )
}
