import * as React from "react"

import { cn } from "@/lib/utils"

/**
 * A native select. The enum setting type has a short, fixed option list, so the
 * platform control is the right one: it is keyboard- and screen-reader-correct
 * with no portal, and it behaves the same in a test as in the browser.
 */
function Select({ className, children, ...props }: React.ComponentProps<"select">) {
  return (
    <select
      data-slot="select"
      className={cn(
        "border-input bg-card flex h-8 w-full rounded-md border px-2 py-1 text-sm transition-[color,box-shadow] outline-none",
        "focus-visible:border-ring focus-visible:ring-ring/40 focus-visible:ring-2",
        "aria-invalid:border-destructive aria-invalid:ring-destructive/20",
        "disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    >
      {children}
    </select>
  )
}

export { Select }
