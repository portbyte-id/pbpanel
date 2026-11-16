import { createRoute } from "@tanstack/react-router"

import { NginxPage } from "@/pages/NginxPage"
import { appRoute } from "@/routes/_app"

export const nginxRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/nginx",
  component: NginxPage,
})
