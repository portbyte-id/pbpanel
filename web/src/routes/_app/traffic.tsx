import { createRoute } from "@tanstack/react-router"

import { TrafficPage } from "@/pages/TrafficPage"
import { appRoute } from "@/routes/_app"

export const trafficRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/traffic",
  component: TrafficPage,
})
