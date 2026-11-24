/** Page sizes a table offers. The first one is the default. */
export const PAGE_SIZES = [10, 25, 50, 100] as const

/** Number of pages for `total` rows; an empty table still has one (empty) page. */
export function pageCount(total: number, pageSize: number): number {
  if (total <= 0 || pageSize <= 0) return 1
  return Math.ceil(total / pageSize)
}

/** The 1-based page clamped into the range that exists. */
export function clampPage(page: number, pages: number): number {
  if (!Number.isFinite(page) || page < 1) return 1
  return Math.min(Math.floor(page), Math.max(1, pages))
}

/**
 * Whether a row matches a free-text query. Every whitespace-separated term must
 * appear, ignoring case, in one of the row's fields. An empty query matches all.
 */
export function matchesQuery(
  fields: ReadonlyArray<string | number | null | undefined>,
  query: string,
): boolean {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean)
  if (terms.length === 0) return true
  const haystack = fields
    .filter((f) => f !== null && f !== undefined)
    .map((f) => String(f).toLowerCase())
    .join("\u0000")
  return terms.every((term) => haystack.includes(term))
}
