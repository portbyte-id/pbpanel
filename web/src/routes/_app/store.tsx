import { createRoute } from "@tanstack/react-router"

import { StorePage } from "@/pages/StorePage"
import { appRoute } from "@/routes/_app"

export const storeRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/store",
  component: StorePage,
})
