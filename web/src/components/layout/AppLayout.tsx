import { Outlet, useRouterState } from "@tanstack/react-router"
import { Menu, Search } from "lucide-react"
import { useEffect, useState } from "react"

import { CommandPalette } from "@/components/layout/CommandPalette"
import { MobileNav } from "@/components/layout/MobileNav"
import { navItemFor } from "@/components/layout/nav"
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
  const current = navItemFor(path)
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [navOpen, setNavOpen] = useState(false)

  // Ctrl/Cmd+K opens the page switcher from anywhere, including inside a form.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
        e.preventDefault()
        setPaletteOpen((open) => !open)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])

  return (
    <div className="bg-background flex min-h-svh">
      <Sidebar />
      <MobileNav open={navOpen} onOpenChange={setNavOpen} />
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="border-border bg-background/90 sticky top-0 z-30 flex h-11 shrink-0 items-center justify-between gap-3 border-b px-4 backdrop-blur pointer-coarse:h-12">
          <div className="flex min-w-0 items-center gap-2 md:items-baseline md:gap-3">
            <button
              type="button"
              onClick={() => setNavOpen(true)}
              aria-label="Open navigation"
              className="text-muted-foreground hover:bg-accent hover:text-foreground -ml-1.5 grid size-8 shrink-0 place-items-center rounded-md md:hidden pointer-coarse:size-10"
            >
              <Menu className="size-5" aria-hidden="true" />
            </button>
            <h1 className="shrink-0 text-md font-semibold tracking-tight">{current?.label ?? "Server Panel"}</h1>
            {current && <p className="text-muted-foreground hidden truncate text-xs md:block">{current.description}</p>}
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <button
              type="button"
              onClick={() => setPaletteOpen(true)}
              aria-label="Go to page"
              className="border-input bg-card text-muted-foreground hover:text-foreground flex h-7 items-center gap-2 rounded-md border pr-1.5 pl-2 text-xs"
            >
              <Search className="size-3.5" aria-hidden="true" />
              <span className="hidden sm:inline">Go to…</span>
              <kbd className="bg-muted text-muted-foreground rounded px-1 font-sans text-xs leading-4">Ctrl K</kbd>
            </button>
            <UserMenu username={me?.username ?? "admin"} />
          </div>
        </header>
        <main className="flex-1 px-4 py-4">
          <Outlet />
        </main>
      </div>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
    </div>
  )
}
