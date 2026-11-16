import { createRoute, redirect } from "@tanstack/react-router"

import { AppLayout } from "@/components/layout/AppLayout"
import { meQuery } from "@/lib/queries"
import { rootRoute } from "@/routes/__root"

/**
 * The signed-in shell. Its `beforeLoad` is the auth guard for every page under
 * it: the session probe runs first, and a failure becomes a redirect to
 * `/login` carrying the page that was asked for.
 *
 * This is a layout route: it has no path of its own, so `/settings` and `/audit`
 * are children rather than nested under a prefix.
 */
export const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "_app",
  beforeLoad: async ({ context, location }) => {
    try {
      await context.queryClient.ensureQueryData(meQuery())
    } catch {
      throw redirect({
        to: "/login",
        search: { redirect: location.href },
        replace: true,
      })
    }
  },
  component: AppLayout,
})
