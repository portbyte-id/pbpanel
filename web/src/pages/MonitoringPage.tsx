import { HistoryCard } from "@/components/HistoryCard"
import { HistoryFilter } from "@/components/HistoryFilter"
import { useSpanParam } from "@/lib/metrics"

/** The host's history on its own page: a preset period, or a custom window of dates, kept in the URL. */
export function MonitoringPage() {
  const [span, pick] = useSpanParam()
  return (
    <div className="flex flex-col gap-3">
      <HistoryFilter span={span} onPreset={pick.preset} onCustom={pick.custom} />
      <HistoryCard span={span} />
    </div>
  )
}
