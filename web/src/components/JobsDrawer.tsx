import { useEffect, useRef, useState } from "react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { JobProgressBar } from "@/components/JobProgressBar"
import { openJobStream, type JobEvent, type JobLine, type JobStreamStatus } from "@/lib/jobs"
import { useJobs } from "@/lib/queries"

interface DrawerLine extends JobLine {
  /** A stable key: the sequence number, plus an ordinal for a replayed duplicate. */
  key: string
}

/**
 * Streams one job's output in a dialog.
 *
 * The stream is opened when a job id arrives and closed when the drawer closes,
 * so a background job does not keep a connection open behind a page nobody is
 * looking at. `Last-Event-ID` resume is handled in `lib/jobs.ts`; this component
 * only keeps what the operator should see.
 */
export function JobsDrawer({
  jobId,
  onClose,
}: {
  jobId: string | null
  onClose: () => void
}) {
  const [lines, setLines] = useState<DrawerLine[]>([])
  const [status, setStatus] = useState<JobStreamStatus>("connecting")
  const [final, setFinal] = useState<JobEvent | null>(null)
  const [truncated, setTruncated] = useState<number | null>(null)
  const bottomRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    if (!jobId) {
      setLines([])
      setStatus("connecting")
      setFinal(null)
      setTruncated(null)
      return
    }

    setLines([])
    setFinal(null)
    setTruncated(null)

    let ordinal = 0
    const stream = openJobStream(jobId, {
      onLine: (line) => {
        ordinal += 1
        setLines((prev) => [...prev, { ...line, key: `${line.seq}-${ordinal}` }])
      },
      onTruncated: (dropped) => setTruncated(dropped),
      onEnd: (job) => setFinal(job),
      onStatus: setStatus,
    })

    return () => stream.close()
  }, [jobId])

  // The live stream carries output, not progress; the job list does, and it is
  // already polled while a job runs.
  const progress = useJobs({ active: true }).data?.find((job) => job.id === jobId)?.progress

  // Follow the tail as lines arrive, which is what a log drawer is for.
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: "end" })
  }, [lines.length])

  return (
    <Dialog open={jobId !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            Job output
            <JobStateBadge status={status} final={final} />
          </DialogTitle>
          <DialogDescription className="font-mono text-xs">{jobId}</DialogDescription>
        </DialogHeader>

        {progress && <JobProgressBar progress={progress} />}

        {truncated !== null && (
          <p className="text-muted-foreground text-xs">
            Earlier output was dropped ({truncated} lines). This stream starts mid-log.
          </p>
        )}

        <pre
          className="bg-muted/40 max-h-[60vh] overflow-auto rounded-md border p-3 font-mono text-xs whitespace-pre-wrap"
          aria-live="polite"
          aria-label="Job output"
        >
          {lines.length === 0 ? (
            <span className="text-muted-foreground">Waiting for output…</span>
          ) : (
            lines.map((line) => (
              <div key={line.key} data-seq={line.seq}>
                {line.text}
              </div>
            ))
          )}
          <div ref={bottomRef} />
        </pre>

        {final?.error && <p className="text-destructive text-sm">{final.error}</p>}

        <div className="flex justify-end">
          <Button variant="outline" onClick={onClose}>
            Close
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}

function JobStateBadge({
  status,
  final,
}: {
  status: JobStreamStatus
  final: JobEvent | null
}) {
  if (final) {
    const failed = final.state === "failed" || final.state === "interrupted"
    return <Badge variant={failed ? "destructive" : "secondary"}>{final.state}</Badge>
  }
  if (status === "error") return <Badge variant="destructive">stream error</Badge>
  return <Badge variant="outline">{status === "open" ? "live" : "connecting"}</Badge>
}
