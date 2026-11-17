import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@/lib/utils"

/**
 * A bordered, softly tinted block for something the operator should notice.
 * The tint is a background only: text stays `foreground`, so contrast does not
 * depend on the tone. Use it instead of hand-written `border-amber-*` classes.
 */
const calloutVariants = cva("rounded-lg border p-4", {
  variants: {
    tone: {
      info: "border-info/40 bg-info/5",
      warning: "border-warning/50 bg-warning/5",
      destructive: "border-destructive/40 bg-destructive/5",
    },
  },
  defaultVariants: { tone: "info" },
})

function Callout({
  className,
  tone,
  ...props
}: React.ComponentProps<"div"> & VariantProps<typeof calloutVariants>) {
  return <div data-slot="callout" className={cn(calloutVariants({ tone }), className)} {...props} />
}

export { Callout, calloutVariants }
