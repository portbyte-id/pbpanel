import { CircleAlert, CircleCheck, X } from "lucide-react"

import { dismissToast, useToasts } from "@/lib/toast"
import { cn } from "@/lib/utils"

/**
 * Renders the toast stack. The container is a polite live region so a screen
 * reader announces each confirmation without taking focus.
 */
export function Toaster() {
  const toasts = useToasts()
  return (
    <div
      role="status"
      aria-live="polite"
      className="pointer-events-none fixed right-4 bottom-4 left-4 z-[60] flex flex-col items-end gap-2 sm:left-auto"
    >
      {toasts.map((t) => (
        <div
          key={t.id}
          className={cn(
            "bg-popover text-popover-foreground pointer-events-auto flex w-full max-w-sm items-start gap-2 rounded-lg border px-3 py-2 text-sm shadow-md",
            "animate-in fade-in-0 slide-in-from-bottom-2 duration-200",
            t.kind === "error" && "border-destructive/50",
          )}
        >
          {t.kind === "success" ? (
            <CircleCheck className="text-success mt-0.5 size-4 shrink-0" aria-hidden="true" />
          ) : (
            <CircleAlert className="text-destructive mt-0.5 size-4 shrink-0" aria-hidden="true" />
          )}
          <p className="min-w-0 flex-1 break-words">{t.message}</p>
          <button
            type="button"
            onClick={() => dismissToast(t.id)}
            aria-label="Dismiss"
            className="text-muted-foreground hover:text-foreground -mt-0.5 -mr-1 grid size-6 shrink-0 place-items-center rounded pointer-coarse:size-10"
          >
            <X className="size-3.5" aria-hidden="true" />
          </button>
        </div>
      ))}
    </div>
  )
}
