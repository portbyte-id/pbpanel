import { createRoute, useNavigate, useSearch } from "@tanstack/react-router"

import { RuntimesPage, runtimeTab, type RuntimeTab } from "@/pages/RuntimesPage"
import { appRoute } from "@/routes/_app"

export const runtimesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/runtimes",
  validateSearch: (search: Record<string, unknown>): { tab?: RuntimeTab } => (search.tab === undefined ? {} : { tab: runtimeTab(search.tab) }),
  component: function Runtimes() {
    const { tab = "nginx" } = useSearch({ from: "/_app/runtimes" })
    const navigate = useNavigate({ from: "/runtimes" })
    return <RuntimesPage tab={tab} onTabChange={(t) => void navigate({ search: { tab: t }, replace: true })} />
  },
})
