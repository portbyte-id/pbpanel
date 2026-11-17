import { useNavigate } from "@tanstack/react-router"
import { KeyRound, LogOut, Monitor, Moon, Sun, UserRound } from "lucide-react"
import { useState } from "react"

import { PasswordDialog } from "@/components/PasswordDialog"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { useLogout } from "@/lib/queries"
import { useTheme, type ThemeMode } from "@/lib/theme"
import { cn } from "@/lib/utils"

const THEMES: { mode: ThemeMode; label: string; icon: typeof Sun }[] = [
  { mode: "system", label: "System", icon: Monitor },
  { mode: "light", label: "Light", icon: Sun },
  { mode: "dark", label: "Dark", icon: Moon },
]

/**
 * The header's account control. One button opens an account dialog (appearance,
 * change password, sign out) so the header stays one row high.
 */
export function UserMenu({ username }: { username: string }) {
  const navigate = useNavigate()
  const logout = useLogout()
  const [theme, setTheme] = useTheme()
  const [accountOpen, setAccountOpen] = useState(false)
  const [passwordOpen, setPasswordOpen] = useState(false)

  const signOut = () => {
    logout.mutate(undefined, {
      onSettled: () => {
        void navigate({ to: "/login", replace: true })
      },
    })
  }

  return (
    <>
      <button
        type="button"
        onClick={() => setAccountOpen(true)}
        aria-label={`Account: ${username}`}
        className="border-input bg-card hover:bg-accent flex h-7 items-center gap-1.5 rounded-md border pr-2.5 pl-1.5 text-xs font-medium"
      >
        <span className="bg-primary/10 text-primary grid size-4.5 place-items-center rounded-full">
          <UserRound className="size-3" aria-hidden="true" />
        </span>
        {username}
      </button>

      <Dialog open={accountOpen} onOpenChange={setAccountOpen}>
        <DialogContent className="sm:max-w-xs">
          <DialogHeader>
            <DialogTitle>{username}</DialogTitle>
            <DialogDescription>Signed in to this panel.</DialogDescription>
          </DialogHeader>

          <div className="flex flex-col gap-1.5">
            <p className="text-muted-foreground text-xs font-medium">Appearance</p>
            <div role="radiogroup" aria-label="Theme" className="bg-muted grid grid-cols-3 gap-0.5 rounded-md p-0.5">
              {THEMES.map(({ mode, label, icon: Icon }) => (
                <button
                  key={mode}
                  type="button"
                  role="radio"
                  aria-checked={theme === mode}
                  onClick={() => setTheme(mode)}
                  className={cn(
                    "flex h-7 items-center justify-center gap-1.5 rounded text-xs font-medium",
                    theme === mode ? "bg-card text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  <Icon className="size-3.5" aria-hidden="true" />
                  {label}
                </button>
              ))}
            </div>
          </div>

          <div className="flex flex-col gap-1.5">
            <Button
              variant="outline"
              className="justify-start"
              onClick={() => {
                setAccountOpen(false)
                setPasswordOpen(true)
              }}
            >
              <KeyRound aria-hidden="true" />
              Change password
            </Button>
            <Button variant="outline" className="justify-start" onClick={signOut} disabled={logout.isPending}>
              <LogOut aria-hidden="true" />
              Sign out
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      <PasswordDialog open={passwordOpen} onOpenChange={setPasswordOpen} />
    </>
  )
}
