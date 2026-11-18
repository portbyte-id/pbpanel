import { createRoute } from "@tanstack/react-router"

import { JobsPage } from "@/pages/JobsPage"
import { appRoute } from "@/routes/_app"

export const jobsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/jobs",
  component: JobsPage,
})
