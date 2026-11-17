import { createRoute } from "@tanstack/react-router"

import { FilesPage } from "@/pages/FilesPage"
import { appRoute } from "@/routes/_app"

export const filesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/files",
  component: FilesPage,
})
