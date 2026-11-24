import { useEffect, useRef, useState } from "react"

import { Progress } from "@/components/ui/progress"
import { api, ApiError } from "@/lib/api"
import { type LoginCheckAnswer, LoginCheckCancelled, solveLoginCheck } from "@/lib/loginCheck"

type Status = "idle" | "loading" | "solving" | "solved" | "error"

const STATUS_TEXT: Record<Status, string> = {
  idle: "Tick the box to confirm you are a person. Your browser then solves a short puzzle.",
  loading: "Preparing the check…",
  solving: "Checking your browser…",
  solved: "Verified. You can sign in now.",
  error: "The check could not run. Tick the box to try again.",
}

/**
 * The check the login page shows after a failed sign-in. Nothing runs until the
 * visitor ticks the box: the puzzle is not requested or solved on render, so a
 * page that loads without a click does no work.
 *
 * `onSolved` gets the answer once it is found, and null whenever there is none
 * (unticked, cancelled, or failed). The parent remounts this component after each
 * refused attempt, because every answer opens one attempt only.
 */
export function LoginCheck({ onSolved }: { onSolved: (answer: LoginCheckAnswer | null) => void }) {
  const [checked, setChecked] = useState(false)
  const [status, setStatus] = useState<Status>("idle")
  const [progress, setProgress] = useState(0)
  const [detail, setDetail] = useState<string | null>(null)
  const controller = useRef<AbortController | null>(null)
  // The callback is read through a ref so the solve keeps the latest parent
  // without restarting when the parent re-renders.
  const onSolvedRef = useRef(onSolved)
  useEffect(() => {
    onSolvedRef.current = onSolved
  }, [onSolved])

  // Leaving the page stops any solve still running.
  useEffect(() => {
    const running = controller
    return () => running.current?.abort()
  }, [])

  const start = async () => {
    const abort = new AbortController()
    controller.current = abort
    setStatus("loading")
    setDetail(null)
    setProgress(0)
    onSolvedRef.current(null)
    try {
      const issued = await api.loginChallenge()
      if (abort.signal.aborted) return
      if (!issued.required || !issued.challenge || issued.difficulty === undefined) {
        // The server no longer asks for a check, so the attempt needs no answer.
        setStatus("solved")
        onSolvedRef.current({ challenge: "", nonce: "" })
        return
      }
      setStatus("solving")
      const nonce = await solveLoginCheck(issued.challenge, issued.difficulty, {
        signal: abort.signal,
        onProgress: setProgress,
      })
      if (abort.signal.aborted) return
      setStatus("solved")
      setProgress(100)
      onSolvedRef.current({ challenge: issued.challenge, nonce })
    } catch (err) {
      if (abort.signal.aborted || err instanceof LoginCheckCancelled) return
      setChecked(false)
      setStatus("error")
      setDetail(err instanceof ApiError ? err.detail : null)
      onSolvedRef.current(null)
    }
  }

  const cancel = () => {
    controller.current?.abort()
    controller.current = null
    setStatus("idle")
    setProgress(0)
    onSolvedRef.current(null)
  }

  const onToggle = (next: boolean) => {
    setChecked(next)
    if (next) void start()
    else cancel()
  }

  const busy = status === "loading" || status === "solving"

  return (
    <div className="bg-muted/40 flex flex-col gap-2 rounded-lg border p-3">
      <label className="flex cursor-pointer items-start gap-3">
        <input
          type="checkbox"
          name="login-check"
          checked={checked}
          onChange={(e) => onToggle(e.target.checked)}
          aria-labelledby="login-check-label"
          aria-describedby="login-check-status"
          className="accent-primary mt-0.5 size-5 shrink-0 cursor-pointer"
        />
        <span className="flex flex-col gap-0.5">
          <span id="login-check-label" className="text-sm font-medium">
            I am not a robot
          </span>
          <span id="login-check-status" role="status" className="text-muted-foreground text-xs">
            {detail ?? STATUS_TEXT[status]}
          </span>
        </span>
      </label>
      {busy && <Progress value={progress} label="Check progress" />}
    </div>
  )
}
