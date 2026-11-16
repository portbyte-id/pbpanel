import { createRoute } from "@tanstack/react-router"

import { PHPPage } from "@/pages/PHPPage"
import { appRoute } from "@/routes/_app"

export const phpRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/php",
  component: PHPPage,
})
