import { Link } from "@tanstack/react-router"
import { Server } from "lucide-react"

import { builtSections, futureSections } from "@/components/layout/nav"
import { cn } from "@/lib/utils"

/**
 * The sidebar. Built sections are links; future ones are disabled buttons whose
 * title says which milestone builds them, so "why can't I click this" is
 * answered by the control itself.
 */
export function Sidebar() {
  return (
    <aside className="border-border bg-sidebar flex w-56 shrink-0 flex-col border-r">
      <div className="flex h-14 items-center gap-2 px-5">
        <Server className="size-4" aria-hidden="true" />
        <span className="text-sm font-semibold tracking-tight">Server Panel</span>
      </div>

      <nav className="flex flex-1 flex-col gap-1 px-3 py-3" aria-label="Sections">
        {builtSections.map((section) => (
          <Link
            key={section.path}
            to={section.path}
            activeOptions={{ exact: false }}
            className={cn(
              "text-muted-foreground hover:bg-accent hover:text-accent-foreground flex items-center gap-2 rounded-md px-2 py-1.5 text-sm",
              "data-[status=active]:bg-accent data-[status=active]:text-accent-foreground data-[status=active]:font-medium",
            )}
          >
            <section.icon className="size-4" aria-hidden="true" />
            {section.label}
          </Link>
        ))}

        <div className="border-border my-2 border-t" role="presentation" />

        {futureSections.map((section) => (
          <button
            key={section.label}
            type="button"
            disabled
            title={`${section.label}: not built yet (${section.milestone})`}
            aria-label={`${section.label} — not built yet`}
            className="text-muted-foreground/60 flex cursor-not-allowed items-center justify-between gap-2 rounded-md px-2 py-1.5 text-sm"
          >
            <span className="flex items-center gap-2">
              <section.icon className="size-4" aria-hidden="true" />
              {section.label}
            </span>
            <span className="text-[0.6875rem] tracking-wide uppercase opacity-70">
              {section.milestone}
            </span>
          </button>
        ))}
      </nav>
    </aside>
  )
}
