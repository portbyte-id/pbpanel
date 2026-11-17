import * as React from "react"

import { cn } from "@/lib/utils"

function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="skeleton"
      aria-hidden="true"
      className={cn("bg-muted animate-pulse rounded-md motion-reduce:animate-none", className)}
      {...props}
    />
  )
}

/**
 * A loading placeholder that keeps the layout from jumping: `rows` bars the
 * height of a table row. It announces `label` politely to screen readers; the
 * bars themselves are hidden from them.
 */
function SkeletonRows({ rows = 3, label }: { rows?: number; label: string }) {
  return (
    <div role="status" aria-live="polite" className="flex flex-col gap-2">
      <span className="sr-only">{label}</span>
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-6" style={{ width: `${100 - i * 12}%` }} />
      ))}
    </div>
  )
}

export { Skeleton, SkeletonRows }
