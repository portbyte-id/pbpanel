import { createRoute } from "@tanstack/react-router"

import { TLSPage } from "@/pages/TLSPage"
import { appRoute } from "@/routes/_app"

export const tlsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/tls",
  component: TLSPage,
})
