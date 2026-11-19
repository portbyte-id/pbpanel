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
import { useChangeUsername } from "@/lib/queries"

/** Matches the server's cap (internal/httpapi/handlers_password.go). */
const MAX_USERNAME_LENGTH = 64

/**
 * The username change form. Like a password change, the server revokes every
 * session, so the only honest next screen is the login page — where the new
 * name is what signs in.
 */
export function UsernameDialog({
  username,
  open,
  onOpenChange,
}: {
  username: string
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const navigate = useNavigate()
  const change = useChangeUsername()
  const [current, setCurrent] = useState("")
  const [next, setNext] = useState(username)
  const [localError, setLocalError] = useState<string | null>(null)

  const reset = () => {
    setCurrent("")
    setNext(username)
    setLocalError(null)
    change.reset()
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()
    setLocalError(null)

    const name = next.trim()
    if (name === "") {
      setLocalError("Enter a username.")
      return
    }
    if (name.length > MAX_USERNAME_LENGTH) {
      setLocalError(`The username must be at most ${MAX_USERNAME_LENGTH} characters.`)
      return
    }
    if (name === username) {
      setLocalError("That is the current username.")
      return
    }

    change.mutate(
      { currentPassword: current, newUsername: name },
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
            <DialogTitle>Change username</DialogTitle>
            <DialogDescription>
              Every signed-in session is revoked, including this one. You will sign in again with
              the new username.
            </DialogDescription>
          </DialogHeader>

          <div className="flex flex-col gap-2">
            <Label htmlFor="new-username">New username</Label>
            <Input
              id="new-username"
              autoComplete="username"
              value={next}
              onChange={(e) => setNext(e.target.value)}
              required
            />
          </div>

          <div className="flex flex-col gap-2">
            <Label htmlFor="username-current-password">Current password</Label>
            <Input
              id="username-current-password"
              type="password"
              autoComplete="current-password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
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
              {change.isPending ? "Saving…" : "Change username"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
