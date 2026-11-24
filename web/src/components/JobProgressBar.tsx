import { Progress } from "@/components/ui/progress"
import type { JobProgress } from "@/lib/jobs"
import { formatBytes } from "@/lib/update"

/**
 * The running step of a job: its label, a bar, and bytes when the step is a
 * transfer of known size. A step with no known total pulses instead of
 * claiming a percentage it cannot know.
 */
export function JobProgressBar({ progress, className }: { progress: JobProgress; className?: string }) {
  const percent =
    progress.total > 0 ? Math.min(100, Math.round((progress.done / progress.total) * 100)) : null
  const bytes =
    progress.total > 0
      ? `${formatBytes(progress.done)} of ${formatBytes(progress.total)}`
      : progress.done > 0
        ? formatBytes(progress.done)
        : null

  return (
    <div className={className}>
      <p className="text-muted-foreground truncate text-xs" title={progress.label}>
        {progress.label}
        {bytes && <span className="tabular-nums"> · {bytes}</span>}
      </p>
      <Progress value={percent} label={progress.label} className="mt-1" />
    </div>
  )
}
