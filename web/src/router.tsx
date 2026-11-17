import { createRouter } from "@tanstack/react-router"
import type { QueryClient } from "@tanstack/react-query"

import { appRoute } from "@/routes/_app"
import { auditRoute } from "@/routes/_app/audit"
import { appsRoute } from "@/routes/_app/apps"
import { cronRoute } from "@/routes/_app/cron"
import { dashboardRoute } from "@/routes/_app/dashboard"
import { appIndexRoute } from "@/routes/_app/index"
import { firewallRoute } from "@/routes/_app/firewall"
import { filesRoute } from "@/routes/_app/files"
import { logsRoute } from "@/routes/_app/logs"
import { nginxRoute } from "@/routes/_app/nginx"
import { phpRoute } from "@/routes/_app/php"
import { runtimesRoute } from "@/routes/_app/runtimes"
import { settingsRoute } from "@/routes/_app/settings"
import { tlsRoute } from "@/routes/_app/tls"
import { siteRoute } from "@/routes/_app/site"
import { sitesRoute } from "@/routes/_app/sites"
import { loginRoute } from "@/routes/login"
import { rootRoute } from "@/routes/__root"

/**
 * The route tree, assembled by hand. The plugin that generates it needs a Vite
 * step that rewrites `routeTree.gen.ts`; five routes do not justify that, and an
 * explicit tree makes the guard's position obvious: `_app` is the parent of
 * every page that needs a session, and `/login` is its sibling.
 */
const routeTree = rootRoute.addChildren([
  loginRoute,
  appRoute.addChildren([
    appIndexRoute,
    dashboardRoute,
    sitesRoute,
    siteRoute,
    filesRoute,
    firewallRoute,
    logsRoute,
    cronRoute,
    nginxRoute,
    phpRoute,
    appsRoute,
    runtimesRoute,
    tlsRoute,
    settingsRoute,
    auditRoute,
  ]),
])

export function createAppRouter(queryClient: QueryClient) {
  return createRouter({
    routeTree,
    context: { queryClient },
    defaultPreload: "intent",
  })
}

export type AppRouter = ReturnType<typeof createAppRouter>

declare module "@tanstack/react-router" {
  interface Register {
    router: AppRouter
  }
}
