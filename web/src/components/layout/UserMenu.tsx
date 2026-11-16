import { useNavigate } from "@tanstack/react-router"
import { LogOut, UserRound } from "lucide-react"
import { useState } from "react"

import { PasswordDialog } from "@/components/PasswordDialog"
import { Button } from "@/components/ui/button"
import { useLogout } from "@/lib/queries"

/** The header's account control: change password, sign out. */
export function UserMenu({ username }: { username: string }) {
  const navigate = useNavigate()
  const logout = useLogout()
  const [passwordOpen, setPasswordOpen] = useState(false)

  const signOut = () => {
    logout.mutate(undefined, {
      onSettled: () => {
        void navigate({ to: "/login", replace: true })
      },
    })
  }

  return (
    <div className="flex items-center gap-3">
      <span className="text-muted-foreground flex items-center gap-1.5 text-sm">
        <UserRound className="size-4" aria-hidden="true" />
        {username}
      </span>
      <Button variant="outline" size="sm" onClick={() => setPasswordOpen(true)}>
        Change password
      </Button>
      <Button variant="ghost" size="sm" onClick={signOut} disabled={logout.isPending}>
        <LogOut className="size-4" aria-hidden="true" />
        Sign out
      </Button>

      <PasswordDialog open={passwordOpen} onOpenChange={setPasswordOpen} />
    </div>
  )
}
