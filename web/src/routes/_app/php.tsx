import { createRoute, redirect } from "@tanstack/react-router"

import { appRoute } from "@/routes/_app"

/** The php page moved into Runtimes; old links and bookmarks land there. */
export const phpRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/php",
  beforeLoad: () => {
    throw redirect({ to: "/runtimes", search: { tab: "php" }, replace: true })
  },
})
