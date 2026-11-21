import { RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ApiError } from "@/lib/api"

/**
 * A failed page load: what went wrong in the server's words, what that likely
 * means, and a retry. A dead end here is worst exactly when a service is down.
 */
export function ErrorState({
  error,
  fallback,
  onRetry,
}: {
  error: unknown
  fallback: string
  onRetry?: () => void
}) {
  const detail = error instanceof ApiError ? error.detail : null
  const output = error instanceof ApiError ? error.output?.trim() : null
  return (
    <div role="alert" className="border-destructive/40 flex flex-col items-start gap-2 rounded-lg border p-4">
      <p className="text-destructive text-sm font-medium">{fallback}</p>
      {detail && <p className="text-muted-foreground text-sm break-words">{detail}</p>}
      {output && (
        <pre className="text-foreground w-full overflow-x-auto font-mono text-xs leading-relaxed whitespace-pre-wrap">
          {output}
        </pre>
      )}
      {!detail && (
        <p className="text-muted-foreground text-sm">
          The panel could not reach its API. If the panel service was restarted, wait a few seconds and try again.
        </p>
      )}
      {onRetry && (
        <Button type="button" variant="outline" size="sm" onClick={onRetry}>
          <RefreshCw aria-hidden="true" />
          Try again
        </Button>
      )}
    </div>
  )
}
