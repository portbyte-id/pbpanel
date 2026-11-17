import { createRoute } from "@tanstack/react-router"

import { FirewallPage } from "@/pages/FirewallPage"
import { appRoute } from "@/routes/_app"

export const firewallRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/firewall",
  component: FirewallPage,
})
