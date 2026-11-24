import { StoreAppsecCard } from "@/components/StoreAppsecCard"

/**
 * The Store: engines and runtimes the panel can install for a host.
 *
 * Today it holds one entry, the second firewall engine. The card is the only
 * place the open-appsec agent is installed from, and it is where the engine
 * picker sends an operator whose host cannot run the engine yet.
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
    </div>
  )
}
