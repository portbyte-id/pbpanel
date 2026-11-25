import { useId, useState, type FormEvent } from "react"

import { RangePicker } from "@/components/RangePicker"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  customProblem,
  localDateString,
  spanKey,
  type CustomSpan,
  type HistorySpan,
  type MetricsRange,
} from "@/lib/metrics"

/**
 * The date filter of the monitoring page: the presets as radios, and a custom
 * window as two calendar dates. The dates are a draft until Apply, so a date
 * half typed never asks the daemon anything.
 */
export function HistoryFilter({
  span,
  onPreset,
  onCustom,
}: {
  span: HistorySpan
  onPreset: (next: MetricsRange) => void
  onCustom: (next: CustomSpan) => void
}) {
  return (
    <div className="bg-card flex flex-wrap items-end gap-x-5 gap-y-3 rounded-xl border p-3 shadow-xs">
      <div className="flex flex-col gap-1">
        <span className="text-muted-foreground text-xs font-medium">Period</span>
        <RangePicker value={typeof span === "string" ? span : null} onChange={onPreset} />
      </div>
      {/* Keyed by the applied window, so a preset or a back step resets the draft dates. */}
      <CustomWindow key={spanKey(span)} span={span} onApply={onCustom} />
    </div>
  )
}

function CustomWindow({ span, onApply }: { span: HistorySpan; onApply: (next: CustomSpan) => void }) {
  const id = useId()
  const applied = typeof span === "string" ? { from: "", to: "" } : span
  const [from, setFrom] = useState(applied.from)
  const [to, setTo] = useState(applied.to)
  const today = localDateString(new Date())
  const draft: CustomSpan | null = from && to ? { from, to } : null
  const problem = draft ? customProblem(draft) : null
  // An applied window that is wrong is explained by the History card; here only a draft being edited.
  const editing = from !== applied.from || to !== applied.to
  const shown = editing ? problem : null

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (draft && !problem) onApply(draft)
  }

  return (
    <form onSubmit={submit} aria-label="Custom date range" className="flex flex-wrap items-end gap-2">
      <div className="flex flex-col gap-1">
        <label htmlFor={`${id}-from`} className="text-muted-foreground text-xs font-medium">
          From
        </label>
        <Input
          id={`${id}-from`}
          type="date"
          max={today}
          value={from}
          onChange={(event) => setFrom(event.target.value)}
          className="h-8 w-auto"
        />
      </div>
      <div className="flex flex-col gap-1">
        <label htmlFor={`${id}-to`} className="text-muted-foreground text-xs font-medium">
          To
        </label>
        <Input
          id={`${id}-to`}
          type="date"
          max={today}
          value={to}
          onChange={(event) => setTo(event.target.value)}
          className="h-8 w-auto"
        />
      </div>
      <Button type="submit" size="sm" disabled={!draft || problem !== null}>
        Apply
      </Button>
      {shown && (
        <p role="alert" className="text-destructive basis-full text-xs">
          {shown}
        </p>
      )}
    </form>
  )
}
