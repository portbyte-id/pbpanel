import { StoreAppsecCard } from "@/components/StoreAppsecCard"
import { StoreClamavCard } from "@/components/StoreClamavCard"

/**
 * The Store: engines and tools the panel can install for a host.
 *
 * Two entries: the second firewall engine, and ClamAV. The open-appsec card is
 * the only place that agent is installed from, and it is where the engine picker
 * sends an operator whose host cannot run the engine yet. The ClamAV card is
 * where the scanner behind the Files page's Scan action comes from.
 */
export function StorePage() {
  return (
    <div className="flex flex-col gap-6">
      <section aria-label="Firewall engines" className="flex flex-col gap-3">
        <div className="flex flex-col gap-0.5">
          <h2 className="text-md font-semibold tracking-tight">Firewall engines</h2>
          <p className="text-muted-foreground text-sm">
            A site&apos;s Firewall tab inspects requests with one engine. ModSecurity is built into the nginx flavor
            that bundles it; the other is installed here.
          </p>
        </div>
        <StoreAppsecCard />
      </section>

      <section aria-label="Security tools" className="flex flex-col gap-3">
        <div className="flex flex-col gap-0.5">
          <h2 className="text-md font-semibold tracking-tight">Security tools</h2>
          <p className="text-muted-foreground text-sm">
            Scan uploaded files and site folders for malware, on demand, from the Files page.
          </p>
        </div>
        <StoreClamavCard />
      </section>
    </div>
  )
}
