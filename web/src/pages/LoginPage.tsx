import { useNavigate, useSearch } from "@tanstack/react-router"
import { useState, type FormEvent } from "react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { ApiError } from "@/lib/api"
import { useLogin } from "@/lib/queries"

/**
 * The login page. It is the one route outside the auth guard, and the only
 * place a 401 is a credential error rather than an expired session.
 */
export function LoginPage() {
  const navigate = useNavigate()
  const login = useLogin()
  const search = useSearch({ from: "/login" })
  const [username, setUsername] = useState("admin")
  const [password, setPassword] = useState("")

  const submit = (event: FormEvent) => {
    event.preventDefault()
    login.mutate(
      { username, password },
      {
        // `redirect` is set when the guard sent the operator here from a page
        // they asked for, so they land back on it instead of the default one.
        onSuccess: () => {
          void navigate({ to: search.redirect ?? "/dashboard", replace: true })
        },
      },
    )
  }

  const error = login.error instanceof ApiError ? login.error.detail : null

  return (
    <main className="bg-background flex min-h-svh items-center justify-center px-4">
      <div className="w-full max-w-sm">
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
              required
            />
          </div>

          <div className="flex flex-col gap-2">
            <Label htmlFor="password">Password</Label>
            <Input
              id="password"
              name="password"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoFocus
              required
            />
          </div>

          {error && (
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          )}

          <Button type="submit" disabled={login.isPending}>
            {login.isPending ? "Signing in…" : "Sign in"}
          </Button>
        </form>
      </div>
    </main>
  )
}
