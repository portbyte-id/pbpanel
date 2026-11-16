import { ApiError } from "@/lib/api"

/**
 * A failed config change.
 *
 * When nginx rejected the change the server sends its output verbatim, and it
 * is shown verbatim: the file and line nginx names are what the operator needs
 * to fix, and a paraphrase would lose them. The sentence above it says what
 * state things are in, which nginx's text does not.
 */
export function ApplyError({ error, fallback }: { error: unknown; fallback: string }) {
  if (!error) return null
  const detail = error instanceof ApiError ? error.detail : fallback
  const output = error instanceof ApiError ? error.output : null

  return (
    <div role="alert" className="border-destructive/40 bg-destructive/5 rounded-md border p-3">
      <p className="text-destructive text-sm font-medium">{detail}</p>
      {output && (
        <pre
          data-testid="validator-output"
          className="text-foreground mt-2 overflow-x-auto font-mono text-xs leading-relaxed whitespace-pre-wrap"
        >
          {output.trimEnd()}
        </pre>
      )}
    </div>
  )
}

/** Warnings from a change that went live anyway. */
export function ApplyWarnings({ warnings }: { warnings: string[] | undefined }) {
  if (!warnings || warnings.length === 0) return null
  return (
    <ul className="text-muted-foreground list-disc pl-5 text-xs" aria-label="Warnings">
      {warnings.map((w) => (
        <li key={w}>{w}</li>
      ))}
    </ul>
  )
}
