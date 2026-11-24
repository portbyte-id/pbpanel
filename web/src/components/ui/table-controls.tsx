import { ChevronLeft, ChevronRight, Search } from "lucide-react"
import { useState, type ReactNode } from "react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select } from "@/components/ui/select"
import { PAGE_SIZES, clampPage, pageCount } from "@/lib/table"

export interface PagedRows<T> {
  /** The rows on the current page. */
  rows: T[]
  /** 1-based current page, always within range. */
  page: number
  pages: number
  pageSize: number
  /** Rows in the whole (already filtered) list. */
  total: number
  /** 1-based index of the first row on the page; 0 when there are no rows. */
  from: number
  /** Index of the last row on the page; 0 when there are no rows. */
  to: number
  setPage: (page: number) => void
  setPageSize: (size: number) => void
}

/**
 * Paginates an already filtered list. Whenever `resetKey` changes (a new search
 * or filter), the list goes back to page 1. Pass the filter values joined into
 * one string, so the reset follows whatever the filters are.
 */
export function usePagedRows<T>(rows: T[], resetKey = ""): PagedRows<T> {
  const [pageSize, setPageSizeState] = useState<number>(PAGE_SIZES[0])
  const [requested, setRequested] = useState(1)
  const [lastKey, setLastKey] = useState(resetKey)
  if (lastKey !== resetKey) {
    setLastKey(resetKey)
    setRequested(1)
  }

  const pages = pageCount(rows.length, pageSize)
  const page = clampPage(requested, pages)
  const start = (page - 1) * pageSize
  const end = Math.min(start + pageSize, rows.length)
  return {
    rows: rows.slice(start, end),
    page,
    pages,
    pageSize,
    total: rows.length,
    from: rows.length === 0 ? 0 : start + 1,
    to: end,
    setPage: setRequested,
    setPageSize: (size) => {
      setPageSizeState(size)
      setRequested(1)
    },
  }
}

/** A row of controls above a table: a free-text search and any filter selects. */
export function TableToolbar({
  search,
  onSearchChange,
  searchLabel = "Search",
  placeholder = "Search",
  children,
}: {
  search: string
  onSearchChange: (value: string) => void
  searchLabel?: string
  placeholder?: string
  /** Extra filters, usually `TableFilter` elements. */
  children?: ReactNode
}) {
  return (
    <div className="mb-3 flex flex-wrap items-end gap-3">
      <div className="relative min-w-48 flex-1">
        <Search
          aria-hidden
          className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2"
        />
        <Input
          type="search"
          aria-label={searchLabel}
          placeholder={placeholder}
          value={search}
          onChange={(e) => onSearchChange(e.target.value)}
          className="pl-8"
        />
      </div>
      {children}
    </div>
  )
}

/** A labelled select that filters a table. `value` "all" means no filter. */
export function TableFilter({
  label,
  value,
  onChange,
  options,
}: {
  label: string
  value: string
  onChange: (value: string) => void
  options: ReadonlyArray<{ value: string; label: string }>
}) {
  const id = `filter-${label.replace(/\s+/g, "-").toLowerCase()}`
  return (
    <div className="flex min-w-36 flex-col gap-1">
      <Label htmlFor={id} className="text-muted-foreground text-xs font-normal">
        {label}
      </Label>
      <Select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="all">All</option>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </Select>
    </div>
  )
}

/** Prev/next, the row range, and a page-size choice. Renders nothing for an empty list. */
export function TablePagination<T>({ pager }: { pager: PagedRows<T> }) {
  if (pager.total === 0) return null
  return (
    <nav
      aria-label="Table pages"
      className="mt-3 flex flex-wrap items-center justify-between gap-3 text-sm"
    >
      <div className="flex items-center gap-2">
        <Label htmlFor="table-page-size" className="text-muted-foreground text-xs font-normal">
          Rows per page
        </Label>
        <Select
          id="table-page-size"
          className="w-20"
          value={String(pager.pageSize)}
          onChange={(e) => pager.setPageSize(Number(e.target.value))}
        >
          {PAGE_SIZES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </Select>
      </div>
      <div className="flex items-center gap-3">
        <span className="text-muted-foreground tabular-nums">
          {pager.from}–{pager.to} of {pager.total}
        </span>
        <div className="flex items-center gap-1">
          <Button
            variant="outline"
            size="icon-sm"
            aria-label="Previous page"
            disabled={pager.page <= 1}
            onClick={() => pager.setPage(pager.page - 1)}
          >
            <ChevronLeft />
          </Button>
          <span className="tabular-nums px-2">
            Page {pager.page} of {pager.pages}
          </span>
          <Button
            variant="outline"
            size="icon-sm"
            aria-label="Next page"
            disabled={pager.page >= pager.pages}
            onClick={() => pager.setPage(pager.page + 1)}
          >
            <ChevronRight />
          </Button>
        </div>
      </div>
    </nav>
  )
}
