import { createRoute } from "@tanstack/react-router"

import { ServicesPage } from "@/pages/ServicesPage"
import { appRoute } from "@/routes/_app"

export const servicesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/services",
  component: ServicesPage,
})
