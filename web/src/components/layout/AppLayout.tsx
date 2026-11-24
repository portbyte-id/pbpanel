import { Outlet, useRouterState } from "@tanstack/react-router"
import { Menu, Search } from "lucide-react"
import { useEffect, useState } from "react"

import { Toaster } from "@/components/Toaster"
import { CommandPalette } from "@/components/layout/CommandPalette"
import { MobileNav } from "@/components/layout/MobileNav"
import { navItemFor } from "@/components/layout/nav"
import { Sidebar } from "@/components/layout/Sidebar"
import { UserMenu } from "@/components/layout/UserMenu"
import { useMe } from "@/lib/queries"

/** Pages that use the whole content width instead of the centred column. */
const FULL_WIDTH_PATHS = ["/files", "/logs", "/waf"]

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
    <div className="bg-background flex h-svh overflow-hidden">
      <a
        href="#main"
        className="bg-primary text-primary-foreground sr-only z-[70] rounded-md px-3 py-2 text-sm font-medium focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        Skip to content
      </a>
      <Sidebar />
      <MobileNav open={navOpen} onOpenChange={setNavOpen} />
      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        <header className="border-border bg-background/90 z-30 flex h-14 shrink-0 items-center justify-between gap-3 border-b px-4 backdrop-blur sm:px-6 pointer-coarse:h-12">
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
              className="border-input bg-card text-muted-foreground hover:text-foreground flex h-7 items-center gap-2 rounded-md border pr-1.5 pl-2 text-xs pointer-coarse:h-10 pointer-coarse:pr-2"
            >
              <Search className="size-3.5" aria-hidden="true" />
              <span className="hidden sm:inline">Go to…</span>
              <kbd className="pointer-coarse:hidden bg-muted text-muted-foreground rounded px-1 font-sans text-xs leading-4">Ctrl K</kbd>
            </button>
            <UserMenu username={me?.username ?? "admin"} />
          </div>
        </header>
        {/* Only <main> scrolls; the header and footer stay pinned to the frame. */}
        <main id="main" tabIndex={-1} className="min-h-0 flex-1 overflow-y-auto px-4 py-6 outline-none sm:px-6">
          {/* Pages are centred in a readable column; the file manager and logs need the width, so they fill it. */}
          {/* /files fills <main> exactly so only its listing scrolls; it needs a definite height here. */}
          <div
            className={
              path.startsWith("/files")
                ? "h-full w-full"
                : FULL_WIDTH_PATHS.some((p) => path.startsWith(p))
                  ? "w-full"
                  : "mx-auto w-full max-w-[84rem]"
            }
          >
            {current && <p className="text-muted-foreground mb-3 text-sm md:hidden">{current.description}</p>}
            <Outlet />
          </div>
        </main>
        <footer className="border-border bg-background text-muted-foreground z-30 flex shrink-0 flex-wrap items-center justify-between gap-x-4 gap-y-1 border-t px-4 py-2 text-xs">
          <span>Server Panel</span>
          <span>{new Date().getFullYear()}</span>
        </footer>
      </div>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
      <Toaster />
    </div>
  )
}
