import { createRoute } from "@tanstack/react-router"

import { LoginPage } from "@/pages/LoginPage"
import { rootRoute } from "@/routes/__root"

export interface LoginSearch {
  /** Where the guard sent the operator from, so login can return them there. */
  redirect?: string
}

export const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  validateSearch: (search: Record<string, unknown>): LoginSearch => ({
    redirect: typeof search.redirect === "string" ? search.redirect : undefined,
  }),
  component: LoginPage,
})
