import { createRoute } from "@tanstack/react-router"

import { SettingsPage } from "@/pages/SettingsPage"
import { appRoute } from "@/routes/_app"

export const settingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings",
  // `?tab=Metrics` opens that group, e.g. from the dashboard's alert card.
  validateSearch: (search: Record<string, unknown>): { tab?: string } =>
    typeof search.tab === "string" && search.tab !== "" ? { tab: search.tab } : {},
  component: SettingsPage,
})
