import { Link } from "@tanstack/react-router"
import { PanelLeftClose, PanelLeftOpen, Server } from "lucide-react"
import { useState } from "react"

import { futureSections, navGroups } from "@/components/layout/nav"
import { cn } from "@/lib/utils"

const KEY = "panel.sidebar.collapsed"

function readCollapsed(): boolean {
  try {
    return window.localStorage.getItem(KEY) === "1"
  } catch {
    return false
  }
}

/**
 * The sidebar: grouped links, collapsible to an icon rail. A page that does not
 * exist yet is a disabled button whose title names the milestone, so "why can't
 * I click this" is answered by the control itself.
 */
export function Sidebar({
  mobile = false,
  onNavigate,
}: {
  /** Inside the mobile drawer: always expanded, no collapse control. */
  mobile?: boolean
  onNavigate?: () => void
}) {
  const [stored, setCollapsed] = useState(readCollapsed)
  const collapsed = !mobile && stored

  const toggle = () => {
    setCollapsed((prev) => {
      try {
        window.localStorage.setItem(KEY, prev ? "0" : "1")
      } catch {
        // Not persisted; the toggle still works for this page.
      }
      return !prev
    })
  }

  return (
    <aside
      data-collapsed={collapsed}
      className={cn(
        "border-sidebar-border bg-sidebar text-sidebar-foreground flex flex-col border-r",
        mobile
          ? "h-full w-full"
          : "sticky top-0 hidden h-svh shrink-0 transition-[width] duration-150 md:flex",
        !mobile && (collapsed ? "w-[4rem]" : "w-48"),
      )}
    >
      <div className={cn("flex h-11 shrink-0 items-center gap-2 px-3", collapsed && "justify-center px-0")}>
        <span className="bg-primary text-primary-foreground grid size-6 shrink-0 place-items-center rounded-md">
          <Server className="size-3.5" aria-hidden="true" />
        </span>
        {!collapsed && <span className="truncate text-sm font-semibold tracking-tight">Server Panel</span>}
      </div>

      <nav className="flex flex-1 flex-col gap-3 overflow-y-auto px-2 py-2" aria-label="Sections">
        {navGroups.map((group) => (
          <div key={group.label} className="flex flex-col gap-px">
            {collapsed ? (
              <div className="bg-sidebar-border mx-2 mb-1 h-px first:hidden" role="presentation" />
            ) : (
              <p className="text-muted-foreground px-2 pb-1 text-xs font-medium tracking-wide uppercase">
                {group.label}
              </p>
            )}
            {group.items.map((item) => (
              <Link
                key={item.path}
                to={item.path}
                title={collapsed ? item.label : undefined}
                aria-label={collapsed ? item.label : undefined}
                activeOptions={{ exact: false }}
                onClick={onNavigate}
                className={cn(
                  "text-sidebar-foreground/80 hover:bg-sidebar-accent hover:text-sidebar-foreground flex h-7 items-center gap-2 rounded-md px-2 text-sm pointer-coarse:h-11",
                  "data-[status=active]:bg-sidebar-accent data-[status=active]:text-foreground data-[status=active]:font-medium",
                  collapsed && "justify-center px-0",
                )}
              >
                <item.icon className="size-4 shrink-0" aria-hidden="true" />
                {!collapsed && <span className="truncate">{item.label}</span>}
              </Link>
            ))}
          </div>
        ))}

        {futureSections.length > 0 && (
          <div className="flex flex-col gap-px">
            {futureSections.map((section) => (
              <button
                key={section.label}
                type="button"
                disabled
                title={`${section.label}: not built yet (${section.milestone})`}
                aria-label={`${section.label} — not built yet`}
                className={cn(
                  "text-muted-foreground/60 flex h-7 cursor-not-allowed items-center justify-between gap-2 rounded-md px-2 text-sm",
                  collapsed && "justify-center px-0",
                )}
              >
                <span className="flex items-center gap-2">
                  <section.icon className="size-4" aria-hidden="true" />
                  {!collapsed && section.label}
                </span>
                {!collapsed && <span className="text-xs tracking-wide uppercase opacity-70">{section.milestone}</span>}
              </button>
            ))}
          </div>
        )}
      </nav>

      <div className={cn("border-sidebar-border border-t p-2", mobile && "hidden")}>
        <button
          type="button"
          onClick={toggle}
          aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
          aria-pressed={collapsed}
          className={cn(
            "text-muted-foreground hover:bg-sidebar-accent hover:text-foreground flex h-7 w-full items-center gap-2 rounded-md px-2 text-xs pointer-coarse:h-11",
            collapsed && "justify-center px-0",
          )}
        >
          {collapsed ? <PanelLeftOpen className="size-4" aria-hidden="true" /> : <PanelLeftClose className="size-4" aria-hidden="true" />}
          {!collapsed && "Collapse"}
        </button>
      </div>
    </aside>
  )
}
