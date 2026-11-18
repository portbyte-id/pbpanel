import { createRoute } from "@tanstack/react-router"

import { DockerPage } from "@/pages/DockerPage"
import { appRoute } from "@/routes/_app"

export const dockerRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/docker",
  component: DockerPage,
})
