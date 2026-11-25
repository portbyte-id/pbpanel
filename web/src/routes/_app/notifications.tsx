import { createRoute } from "@tanstack/react-router"

import { NotificationsPage } from "@/pages/NotificationsPage"
import { appRoute } from "@/routes/_app"

export const notificationsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/notifications",
  component: NotificationsPage,
})
