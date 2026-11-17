import { createRoute } from "@tanstack/react-router"

import { CronPage } from "@/pages/CronPage"
import { appRoute } from "@/routes/_app"

export const cronRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/cron",
  component: CronPage,
})
