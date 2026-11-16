import { useNavigate } from "@tanstack/react-router"
import { useState, type FormEvent } from "react"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { ApiError } from "@/lib/api"
import { useChangePassword } from "@/lib/queries"

/** Matches the server's floor (internal/httpapi/handlers_password.go). */
const MIN_PASSWORD_LENGTH = 12

/**
 * The password change form. On success the server has revoked every session
 * including this one, so the only honest next screen is the login page.
 */
export function PasswordDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const navigate = useNavigate()
  const change = useChangePassword()
  const [current, setCurrent] = useState("")
  const [next, setNext] = useState("")
  const [confirm, setConfirm] = useState("")
  const [localError, setLocalError] = useState<string | null>(null)

  const reset = () => {
    setCurrent("")
    setNext("")
    setConfirm("")
    setLocalError(null)
    change.reset()
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()
    setLocalError(null)

    if (next.length < MIN_PASSWORD_LENGTH) {
      setLocalError(`The new password must be at least ${MIN_PASSWORD_LENGTH} characters.`)
      return
    }
    if (next !== confirm) {
      setLocalError("The two new passwords do not match.")
      return
    }

    change.mutate(
      { currentPassword: current, newPassword: next },
      {
        onSuccess: () => {
          reset()
          onOpenChange(false)
          void navigate({ to: "/login", replace: true })
        },
      },
    )
  }

  // The server's own sentence is the most accurate message available: it knows
  // whether the current password was wrong or the address got throttled.
  const serverError = change.error instanceof ApiError ? change.error.detail : null
  const error = localError ?? serverError

  return (
    <Dialog
      open={open}
      onOpenChange={(value) => {
        if (!value) reset()
        onOpenChange(value)
      }}
    >
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Change password</DialogTitle>
            <DialogDescription>
              Every signed-in session is revoked, including this one. You will sign in again.
            </DialogDescription>
          </DialogHeader>

          <div className="flex flex-col gap-2">
            <Label htmlFor="current-password">Current password</Label>
            <Input
              id="current-password"
              type="password"
              autoComplete="current-password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              required
            />
          </div>

          <div className="flex flex-col gap-2">
            <Label htmlFor="new-password">New password</Label>
            <Input
              id="new-password"
              type="password"
              autoComplete="new-password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
              aria-describedby="new-password-hint"
              required
            />
            <p id="new-password-hint" className="text-muted-foreground text-xs">
              At least {MIN_PASSWORD_LENGTH} characters. A longer passphrase is stronger than a
              short one with symbols.
            </p>
          </div>

          <div className="flex flex-col gap-2">
            <Label htmlFor="confirm-password">Confirm new password</Label>
            <Input
              id="confirm-password"
              type="password"
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              required
            />
          </div>

          {error && (
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={change.isPending}>
              {change.isPending ? "Saving…" : "Change password"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
