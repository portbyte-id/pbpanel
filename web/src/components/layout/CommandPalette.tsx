import { useNavigate } from "@tanstack/react-router"
import { CornerDownLeft, Search } from "lucide-react"
import { useEffect, useMemo, useState } from "react"

import { builtSections } from "@/components/layout/nav"
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog"
import { cn } from "@/lib/utils"

/**
 * Jump to any page by name. The pages are the whole navigation, so this is a
 * filter over the same list the sidebar shows; nothing here reaches the server.
 */
export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const navigate = useNavigate()
  const [query, setQuery] = useState("")
  const [active, setActive] = useState(0)

  useEffect(() => {
    if (open) {
      setQuery("")
      setActive(0)
    }
  }, [open])

  const results = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (q === "") return builtSections
    return builtSections.filter((item) =>
      `${item.label} ${item.description} ${item.keywords ?? ""}`.toLowerCase().includes(q),
    )
  }, [query])

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
            autoFocus
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
            placeholder="Go to a page…"
            aria-label="Search pages"
            className="placeholder:text-muted-foreground h-10 w-full bg-transparent text-sm outline-none"
          />
        </div>
        <ul className="max-h-72 overflow-y-auto p-1" role="listbox" aria-label="Pages">
          {results.length === 0 && <li className="text-muted-foreground px-3 py-6 text-center text-sm">No page matches “{query}”.</li>}
          {results.map((item, i) => (
            <li key={item.path} role="option" aria-selected={i === active}>
              <button
                type="button"
                onClick={() => go(item.path)}
                onMouseMove={() => setActive(i)}
                className={cn(
                  "flex w-full items-center gap-2.5 rounded-md px-2.5 py-1.5 text-left text-[0.8125rem]",
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
