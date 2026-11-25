import { useId } from "react"

import { RANGES, RANGE_NAMES, type MetricsRange } from "@/lib/metrics"
import { cn } from "@/lib/utils"

/**
 * The history range as a segmented radio group. Native radios, so the arrow
 * keys, the single tab stop and the screen reader's "2 of 6" come from the
 * platform. The value belongs to the caller; the pages keep it in the URL with
 * `useRangeParam`.
 */
export function RangePicker({
  value,
  onChange,
  label = "History range",
}: {
  value: MetricsRange
  onChange: (next: MetricsRange) => void
  label?: string
}) {
  const name = useId()
  return (
    <div role="radiogroup" aria-label={label} className="bg-muted flex gap-0.5 rounded-md p-0.5">
      {RANGES.map((r) => (
        <label
          key={r}
          className={cn(
            "has-[:focus-visible]:ring-ring/50 flex h-6 cursor-pointer items-center rounded px-2 text-xs font-medium has-[:focus-visible]:ring-2",
            value === r ? "bg-card text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
          )}
        >
          <input
            type="radio"
            name={name}
            value={r}
            checked={value === r}
            aria-label={RANGE_NAMES[r]}
            onChange={() => onChange(r)}
            className="sr-only"
          />
          <span aria-hidden="true">{r}</span>
        </label>
      ))}
    </div>
  )
}
