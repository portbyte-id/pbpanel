import { ShieldAlert } from "lucide-react"

import { Callout } from "@/components/ui/callout"
import { cn } from "@/lib/utils"

/**
 * The open-appsec tracking statement, exactly as the API sends it.
 *
 * `warning` is the server's `internal/appsec.Warning` constant, and this
 * component does nothing to it: no truncation, no ellipsis, no rewriting. That
 * is the point of the D5 decision (docs/design/open-appsec-plan.md §6.1) — the
 * operator accepted this engine knowing it holds a TLS connection to Check
 * Point's cloud, and that acceptance is honest only if every screen that offers
 * the engine shows the same sentence the API holds.
 *
 * Wherever the engine can be chosen or installed, this is rendered: the Store
 * card, the install confirmation, and the engine picker. `data-verbatim` is a
 * test hook and an aid to the reader; the text itself is always the prop.
 */
export function AppsecWarning({
  warning,
  className,
  compact = false,
}: {
  /** The API's `warning` field. Rendered as-is; never a local copy. */
  warning: string
  className?: string
  /** The inline form the engine picker uses, which is a line under the option. */
  compact?: boolean
}) {
  if (warning === "") return null
  return (
    <Callout
      tone="warning"
      role="note"
      aria-label="open-appsec tracking warning"
      data-verbatim="open-appsec-warning"
      className={cn(compact ? "flex items-start gap-2 p-2" : "flex items-start gap-3", className)}
    >
      <ShieldAlert
        className={cn("text-warning shrink-0", compact ? "mt-0.5 size-4" : "mt-0.5 size-5")}
        aria-hidden="true"
      />
      <p className={cn("min-w-0", compact ? "text-xs" : "text-sm")}>{warning}</p>
    </Callout>
  )
}
