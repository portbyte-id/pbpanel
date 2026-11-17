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
export function Sidebar() {
  const [collapsed, setCollapsed] = useState(readCollapsed)

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
        "border-sidebar-border bg-sidebar text-sidebar-foreground sticky top-0 flex h-svh shrink-0 flex-col border-r transition-[width] duration-150",
        collapsed ? "w-[3.25rem]" : "w-48",
      )}
    >
      <div className={cn("flex h-11 shrink-0 items-center gap-2 px-3", collapsed && "justify-center px-0")}>
        <span className="bg-primary text-primary-foreground grid size-6 shrink-0 place-items-center rounded-md">
          <Server className="size-3.5" aria-hidden="true" />
        </span>
        {!collapsed && <span className="truncate text-[0.8125rem] font-semibold tracking-tight">Server Panel</span>}
      </div>

      <nav className="flex flex-1 flex-col gap-3 overflow-y-auto px-2 py-2" aria-label="Sections">
        {navGroups.map((group) => (
          <div key={group.label} className="flex flex-col gap-px">
            {collapsed ? (
              <div className="bg-sidebar-border mx-2 mb-1 h-px first:hidden" role="presentation" />
            ) : (
              <p className="text-muted-foreground px-2 pb-1 text-[0.6875rem] font-medium tracking-wide uppercase">
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
                className={cn(
                  "text-sidebar-foreground/80 hover:bg-sidebar-accent hover:text-sidebar-foreground flex h-7 items-center gap-2 rounded-md px-2 text-[0.8125rem]",
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
                  "text-muted-foreground/60 flex h-7 cursor-not-allowed items-center justify-between gap-2 rounded-md px-2 text-[0.8125rem]",
                  collapsed && "justify-center px-0",
                )}
              >
                <span className="flex items-center gap-2">
                  <section.icon className="size-4" aria-hidden="true" />
                  {!collapsed && section.label}
                </span>
                {!collapsed && <span className="text-[0.625rem] tracking-wide uppercase opacity-70">{section.milestone}</span>}
              </button>
            ))}
          </div>
        )}
      </nav>

      <div className="border-sidebar-border border-t p-2">
        <button
          type="button"
          onClick={toggle}
          aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
          aria-pressed={collapsed}
          className={cn(
            "text-muted-foreground hover:bg-sidebar-accent hover:text-foreground flex h-7 w-full items-center gap-2 rounded-md px-2 text-xs",
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
