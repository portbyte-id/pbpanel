import { cn } from "@/lib/utils"

/**
 * A progress bar. A number is a determinate fill; null means the work has no
 * measurable progress yet, so the bar is a pulsing band instead of a lie.
 */
function Progress({
  value,
  label,
  className,
}: {
  value: number | null
  label: string
  className?: string
}) {
  return (
    <div
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={value ?? undefined}
      aria-valuetext={value === null ? "in progress" : `${value}%`}
      data-slot="progress"
      className={cn("bg-muted relative h-2 w-full overflow-hidden rounded-full", className)}
    >
      {value === null ? (
        <div className="bg-primary absolute inset-y-0 left-0 w-full animate-pulse rounded-full opacity-60" />
      ) : (
        <div
          className="bg-primary h-full rounded-full transition-[width] duration-300"
          style={{ width: `${value}%` }}
        />
      )}
    </div>
  )
}

export { Progress }
