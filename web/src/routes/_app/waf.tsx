import { createRoute } from "@tanstack/react-router"

import { WafPage } from "@/pages/WafPage"
import { appRoute } from "@/routes/_app"

export const wafRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/waf",
  component: WafPage,
})
