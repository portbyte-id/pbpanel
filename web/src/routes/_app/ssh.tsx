import { createRoute } from "@tanstack/react-router"

import { SSHPage } from "@/pages/SSHPage"
import { appRoute } from "@/routes/_app"

export const sshRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/ssh",
  component: SSHPage,
})
