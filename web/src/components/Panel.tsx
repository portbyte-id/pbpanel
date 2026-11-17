import type { ReactNode } from "react"

import { cn } from "@/lib/utils"

/**
 * A titled block: a one-row header (title, optional note and action) over a
 * body. The page's unit of grouping, in place of loose headings over loose
 * content. It does not nest; a panel inside a panel is a sign the page wants a
 * table or a dialog instead.
 */
export function Panel({
  title,
  note,
  action,
  children,
  className,
  bodyClassName,
}: {
  title: string
  /** Quiet text beside the title, e.g. a count or a stream status. */
  note?: ReactNode
  /** A control at the right end of the header. */
  action?: ReactNode
  children: ReactNode
  className?: string
  bodyClassName?: string
}) {
  return (
    <section aria-label={title} className={cn("bg-card flex min-w-0 flex-col rounded-lg border", className)}>
      <div className="border-border flex h-9 shrink-0 items-center justify-between gap-3 border-b px-3">
        <div className="flex min-w-0 items-baseline gap-2">
          <h2 className="text-sm font-semibold">{title}</h2>
          {note && <p className="text-muted-foreground truncate text-xs">{note}</p>}
        </div>
        {action}
      </div>
      <div className={cn("min-h-0 flex-1", bodyClassName)}>{children}</div>
    </section>
  )
}
