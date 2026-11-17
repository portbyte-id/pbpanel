import { ServicesSection } from "@/pages/DashboardPage"

/**
 * Every service, container and app with its actions. The dashboard shows the
 * eight most urgent rows; this page is the whole list, with a name filter and a
 * state filter, and it shares the dashboard's confirm-before-stop behaviour.
 */
export function ServicesPage() {
  return (
    <div className="max-w-5xl">
      <ServicesSection filters />
    </div>
  )
}
