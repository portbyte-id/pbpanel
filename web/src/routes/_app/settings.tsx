import { createRoute } from "@tanstack/react-router"

import { SettingsPage } from "@/pages/SettingsPage"
import { appRoute } from "@/routes/_app"

export const settingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings",
  component: SettingsPage,
})
