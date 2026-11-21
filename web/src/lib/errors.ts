import { ApiError } from "@/lib/api"

/**
 * One sentence for a failed request, with the server's own words when it sent
 * any.
 *
 * Every API failure arrives as an `ApiError` carrying the envelope's `detail`,
 * and config changes also carry the validator's `output` (`nginx -t`). Both are
 * what the operator needs to fix the problem, so both are shown; the fallback is
 * only for a failure that never reached the server or arrived without a body,
 * such as a dropped connection.
 */
export function describeError(error: unknown, fallback: string): string {
  if (!(error instanceof ApiError)) return fallback
  const output = error.output?.trim()
  return output ? `${error.detail}\n\n${output}` : error.detail
}
