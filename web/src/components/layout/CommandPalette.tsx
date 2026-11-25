import { useNavigate } from "@tanstack/react-router"
import { CornerDownLeft, Globe, Search } from "lucide-react"
import { useEffect, useMemo, useState } from "react"

import { builtSections, type NavItem } from "@/components/layout/nav"
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog"
import { useSites } from "@/lib/queries"
import { canAutoFocus, cn } from "@/lib/utils"

/**
 * Jump to any page or site by name. Pages are the whole navigation; sites come
 * from the cached site list, so opening the palette sends no request of its own.
 */
export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const navigate = useNavigate()
  const [query, setQuery] = useState("")
  const [active, setActive] = useState(0)
  const sites = useSites()

  useEffect(() => {
    if (open) {
      setQuery("")
      setActive(0)
    }
  }, [open])

  const entries = useMemo<NavItem[]>(() => {
    const siteEntries: NavItem[] = (sites.data ?? []).map((s) => ({
      label: s.domain,
      icon: Globe,
      path: `/sites/${s.domain}`,
      description: "Site",
      keywords: s.aliases.join(" "),
    }))
    return [...builtSections, ...siteEntries]
  }, [sites.data])

  const results = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (q === "") return builtSections
    return entries.filter((item) =>
      `${item.label} ${item.description} ${item.keywords ?? ""}`.toLowerCase().includes(q),
    )
  }, [query, entries])

  const go = (path: string) => {
    onOpenChange(false)
    void navigate({ to: path })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent showCloseButton={false} className="top-[20%] translate-y-0 gap-0 overflow-hidden p-0 sm:max-w-lg">
        <DialogTitle className="sr-only">Go to page</DialogTitle>
        <DialogDescription className="sr-only">Type to filter pages, arrow keys to choose, Enter to open.</DialogDescription>
        <div className="border-border flex items-center gap-2 border-b px-3">
          <Search className="text-muted-foreground size-4 shrink-0" aria-hidden="true" />
          <input
            autoFocus={canAutoFocus()}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value)
              setActive(0)
            }}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault()
                setActive((i) => Math.min(i + 1, results.length - 1))
              } else if (e.key === "ArrowUp") {
                e.preventDefault()
                setActive((i) => Math.max(i - 1, 0))
              } else if (e.key === "Enter" && results[active]) {
                e.preventDefault()
                go(results[active].path)
              }
            }}
            placeholder="Go to a page or site…"
            aria-label="Search pages and sites"
            className="placeholder:text-muted-foreground h-10 w-full bg-transparent text-sm outline-none"
          />
        </div>
        <ul className="max-h-72 overflow-y-auto overscroll-contain p-1" role="listbox" aria-label="Pages and sites">
          {results.length === 0 && <li className="text-muted-foreground px-3 py-6 text-center text-sm">Nothing matches “{query}”.</li>}
          {results.map((item, i) => (
            <li key={item.path} role="option" aria-selected={i === active}>
              <button
                type="button"
                onClick={() => go(item.path)}
                onMouseMove={() => setActive(i)}
                className={cn(
                  "flex w-full items-center gap-2.5 rounded-md px-2.5 py-1.5 text-left text-sm",
                  i === active ? "bg-accent text-accent-foreground" : "text-foreground",
                )}
              >
                <item.icon className="text-muted-foreground size-4 shrink-0" aria-hidden="true" />
                <span className="font-medium">{item.label}</span>
                <span className="text-muted-foreground truncate text-xs">{item.description}</span>
                {i === active && <CornerDownLeft className="text-muted-foreground ml-auto size-3.5 shrink-0" aria-hidden="true" />}
              </button>
            </li>
          ))}
        </ul>
      </DialogContent>
    </Dialog>
  )
}
