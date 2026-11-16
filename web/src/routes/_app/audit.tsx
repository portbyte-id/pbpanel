import { createRoute } from "@tanstack/react-router"

import { AuditPage } from "@/pages/AuditPage"
import { appRoute } from "@/routes/_app"

export const auditRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/audit",
  component: AuditPage,
})
