import { Outlet, useRouterState } from "@tanstack/react-router"

import { Sidebar } from "@/components/layout/Sidebar"
import { UserMenu } from "@/components/layout/UserMenu"
import { useMe } from "@/lib/queries"

/**
 * The signed-in frame: sidebar, header and the routed page.
 *
 * The layout is only mounted behind the auth guard (see `routes/__root.tsx`),
 * so `useMe` here reads a query the guard already resolved.
 */
export function AppLayout() {
  const { data: me } = useMe()
  const path = useRouterState({ select: (state) => state.location.pathname })
  const current = pageTitle(path)

  return (
    <div className="flex min-h-svh bg-background">
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="border-border flex h-14 shrink-0 items-center justify-between border-b px-6">
          <h1 className="text-base font-semibold">{current}</h1>
          <UserMenu username={me?.username ?? "admin"} />
        </header>
        <main className="flex-1 px-6 py-6">
          <Outlet />
        </main>
      </div>
    </div>
  )
}

function pageTitle(path: string): string {
  if (path.startsWith("/settings")) return "Settings"
  if (path.startsWith("/audit")) return "Audit log"
  return "Server Panel"
}
