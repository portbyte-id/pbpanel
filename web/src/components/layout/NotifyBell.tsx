import { useEffect, useState } from "react"
import { Link } from "@tanstack/react-router"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Bell } from "lucide-react"

import {
  markNotifyRead,
  notifyEvents,
  notifySummary,
  openNotifyStream,
  type NotifyEvent,
} from "@/lib/notify"

/** The header bell: an unread badge, a short list, and a link to the full page. */
export function NotifyBell() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)

  const summary = useQuery({
    queryKey: ["notify", "summary"],
    queryFn: () => notifySummary(),
    refetchInterval: 60_000,
    retry: false,
  })

  const recent = useQuery({
    queryKey: ["notify", "events", "bell"],
    queryFn: () => notifyEvents({ limit: 10 }),
    enabled: open,
    retry: false,
  })

  // A new event, or a reconnect, refetches what the bell shows.
  useEffect(() => {
    const close = openNotifyStream(
      () => void qc.invalidateQueries({ queryKey: ["notify"] }),
      () => void qc.invalidateQueries({ queryKey: ["notify"] }),
    )
    return close
  }, [qc])

  const unread = summary.data?.unread ?? 0
  const critical = summary.data?.open_critical ?? 0

  async function markAll() {
    await markNotifyRead("all")
    await qc.invalidateQueries({ queryKey: ["notify"] })
  }

  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-label={unread > 0 ? `Notifications, ${unread} unread` : "Notifications"}
        className="border-input bg-card text-muted-foreground hover:text-foreground relative flex size-7 items-center justify-center rounded-md border pointer-coarse:size-10"
      >
        <Bell className="size-3.5" aria-hidden="true" />
        {unread > 0 && (
          <span
            data-testid="notify-badge"
            className={`absolute -top-1 -right-1 min-w-4 rounded-full px-1 text-[10px] leading-4 text-white ${
              critical > 0 ? "bg-red-600" : "bg-amber-600"
            }`}
          >
            {unread > 99 ? "99+" : unread}
          </span>
        )}
      </button>

      {open && (
        <div
          role="region"
          aria-label="Latest notifications"
          className="bg-card border-border absolute right-0 z-50 mt-2 w-80 rounded-md border p-2 shadow-lg"
        >
          <div className="mb-2 flex items-center justify-between">
            <span className="text-sm font-medium">Notifications</span>
            <button
              type="button"
              onClick={markAll}
              disabled={unread === 0}
              className="text-muted-foreground hover:text-foreground text-xs disabled:opacity-50"
            >
              Mark all read
            </button>
          </div>
          {recent.isLoading && <p className="text-muted-foreground text-xs">Loading…</p>}
          {recent.data?.length === 0 && <p className="text-muted-foreground text-xs">Nothing yet.</p>}
          <ul className="max-h-80 space-y-1 overflow-y-auto">
            {recent.data?.map((ev: NotifyEvent) => (
              <li key={ev.id} className={`rounded px-2 py-1 text-xs ${ev.read_at ? "" : "bg-muted"}`}>
                <div className="flex items-center gap-2">
                  <span className="font-medium">{ev.resolved ? "Resolved" : ev.severity}</span>
                  <span className="text-muted-foreground truncate">{ev.subject}</span>
                </div>
              </li>
            ))}
          </ul>
          <Link to="/notifications" onClick={() => setOpen(false)} className="text-primary mt-2 block text-xs">
            Open all notifications
          </Link>
        </div>
      )}
    </div>
  )
}
