import { useNavigate, useSearch } from "@tanstack/react-router"
import { Eye, EyeOff } from "lucide-react"
import { useEffect, useState, type FormEvent } from "react"

import { LoginCheck } from "@/components/LoginCheck"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { api, ApiError } from "@/lib/api"
import type { LoginCheckAnswer } from "@/lib/loginCheck"
import { useLogin } from "@/lib/queries"

/**
 * A refused attempt that leaves a check on the next one: bad credentials (401),
 * a missing or spent check (403), or a lockout (429). Each of them means the
 * address now has a failure on record, so the check comes back.
 */
function refusalNeedsCheck(err: unknown): boolean {
  return err instanceof ApiError && [401, 403, 429].includes(err.status)
}

/**
 * The login page. It is the one route outside the auth guard, and the only
 * place a 401 is a credential error rather than an expired session.
 */
export function LoginPage() {
  const navigate = useNavigate()
  const login = useLogin()
  const search = useSearch({ from: "/login" })
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [showPassword, setShowPassword] = useState(false)
  // Once a sign-in has failed from this address, every attempt carries a solved
  // browser check. `solved` is that answer, `attempt` restarts the check unticked.
  const [checkRequired, setCheckRequired] = useState(false)
  const [solved, setSolved] = useState<LoginCheckAnswer | null>(null)
  const [attempt, setAttempt] = useState(0)

  // A reload after a failure still shows the check. A failed lookup is not
  // fatal: the server enforces the check whether or not the page knows of it.
  useEffect(() => {
    let live = true
    api.loginChallenge().then(
      (issued) => {
        if (live && issued.required) setCheckRequired(true)
      },
      () => undefined,
    )
    return () => {
      live = false
    }
  }, [])

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (checkRequired && solved === null) return
    login.mutate(
      { username, password, check: checkRequired ? (solved ?? undefined) : undefined },
      {
        // `redirect` is set when the guard sent the operator here from a page
        // they asked for, so they land back on it instead of the default one.
        onSuccess: () => {
          void navigate({ to: search.redirect ?? "/dashboard", replace: true })
        },
        onError: (err) => {
          if (refusalNeedsCheck(err)) {
            // The answer just used is spent, so the next attempt needs a new one.
            setCheckRequired(true)
            setSolved(null)
            setAttempt((n) => n + 1)
          }
        },
      },
    )
  }

  const error = login.error instanceof ApiError ? login.error.detail : null

  return (
    <main className="bg-background flex min-h-svh items-center justify-center px-4">
      <div className="bg-card w-full max-w-sm rounded-xl border p-8 shadow-lg">
        <div className="mb-6 flex flex-col gap-1">
          <h1 className="text-xl font-semibold tracking-tight">Server Panel</h1>
          <p className="text-muted-foreground text-sm">Sign in to manage this server.</p>
        </div>

        <form onSubmit={submit} className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="username">Username</Label>
            <Input
              id="username"
              name="username"
              autoComplete="username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoFocus
              required
            />
          </div>

          <div className="flex flex-col gap-2">
            <Label htmlFor="password">Password</Label>
            <div className="relative">
              <Input
                id="password"
                name="password"
                type={showPassword ? "text" : "password"}
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
                className="pr-10"
              />
              <button
                type="button"
                onClick={() => setShowPassword((v) => !v)}
                aria-label={showPassword ? "Hide password" : "Show password"}
                aria-pressed={showPassword}
                className="text-muted-foreground hover:text-foreground absolute top-0 right-0 grid h-9 w-9 place-items-center rounded-md pointer-coarse:h-10"
              >
                {showPassword ? <EyeOff className="size-4" aria-hidden="true" /> : <Eye className="size-4" aria-hidden="true" />}
              </button>
            </div>
          </div>

          {checkRequired && <LoginCheck key={attempt} onSolved={setSolved} />}

          {error && (
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          )}

          <Button
            type="submit"
            size="lg"
            className="w-full"
            disabled={login.isPending || (checkRequired && solved === null)}
          >
            {login.isPending ? "Signing in…" : "Sign in"}
          </Button>
        </form>
      </div>
    </main>
  )
}
