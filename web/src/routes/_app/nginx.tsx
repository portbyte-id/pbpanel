import { createRoute, redirect } from "@tanstack/react-router"

import { appRoute } from "@/routes/_app"

/** The nginx page moved into Runtimes; old links and bookmarks land there. */
export const nginxRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/nginx",
  beforeLoad: () => {
    throw redirect({ to: "/runtimes", search: { tab: "nginx" }, replace: true })
  },
})
