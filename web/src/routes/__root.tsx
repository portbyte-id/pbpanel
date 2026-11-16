import { createRootRouteWithContext, Outlet } from "@tanstack/react-router"
import type { QueryClient } from "@tanstack/react-query"

/**
 * The root route. It renders the router's outlet and nothing else: the auth
 * guard lives on the layout route below it, so `/login` stays reachable.
 *
 * The context is the query client, which is what lets a `beforeLoad` guard run
 * the session probe through the same cache the pages read.
 */
export interface RouterContext {
  queryClient: QueryClient
}

export const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: RootComponent,
})

function RootComponent() {
  return <Outlet />
}
