import { useId, useState } from "react"

import { ApplyError } from "@/components/ApplyError"
import { parseAliases } from "@/components/CreateSiteDialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import type { SiteDetail, SiteFeature, UpdateSiteInput } from "@/lib/api"

const featureLabel: Record<SiteFeature["feature"], string> = {
  php: "PHP version and pool",
  listen: "Ports",
  names: "Domains",
  acme: "Certificate challenges",
  proxy: "Proxy target",
}

function featureOf(detail: SiteDetail, name: SiteFeature["feature"]): SiteFeature | undefined {
  return detail.features.find((f) => f.feature === name)
}

/**
 * Why a control is switched off, in the place the control is.
 *
 * A feature is detached when the site file no longer has the `include` line for
 * the file the control writes. The panel does not fight that edit: it disables
 * the control and says which line would bring it back.
 */
function Detached({ feature, id }: { feature: SiteFeature; id: string }) {
  return (
    <p id={id} className="text-muted-foreground text-xs">
      Switched off: {feature.reason ?? "the site file no longer includes this control's file."}
    </p>
  )
}

/**
 * The controls the panel offers for one site. Each writes its own include file
 * and nothing else; `onSave` is called with only the field that changed.
 */
export function SiteControls({
  detail,
  onSave,
  pending,
  error,
}: {
  detail: SiteDetail
  onSave: (input: UpdateSiteInput) => void
  pending: boolean
  error: unknown
}) {
  const id = useId()
  const names = featureOf(detail, "names")
  const proxy = featureOf(detail, "proxy")
  const namesOff = names !== undefined && !names.attached
  const proxyOff = proxy !== undefined && !proxy.attached

  const [aliases, setAliases] = useState(detail.aliases.join(" "))
  const [target, setTarget] = useState(detail.proxy_target ?? "")

  const aliasesChanged = parseAliases(aliases).join(" ") !== detail.aliases.join(" ")
  const targetChanged = target.trim() !== (detail.proxy_target ?? "")

  return (
    <div className="flex flex-col gap-6">
      <form
        className="flex flex-col gap-1.5"
        onSubmit={(e) => {
          e.preventDefault()
          onSave({ aliases: parseAliases(aliases) })
        }}
      >
        <Label htmlFor={`${id}-aliases`}>Other domains</Label>
        <div className="flex gap-2">
          <Input
            id={`${id}-aliases`}
            value={aliases}
            onChange={(e) => setAliases(e.target.value)}
            placeholder="www.example.com"
            disabled={namesOff}
            autoComplete="off"
            spellCheck={false}
            aria-describedby={`${id}-aliases-help`}
          />
          <Button type="submit" variant="outline" disabled={namesOff || pending || !aliasesChanged}>
            Save domains
          </Button>
        </div>
        {namesOff && names ? (
          <Detached feature={names} id={`${id}-aliases-help`} />
        ) : (
          <p id={`${id}-aliases-help`} className="text-muted-foreground text-xs">
            Answered in addition to {detail.domain}. Separate several with spaces or commas.
          </p>
        )}
      </form>

      {detail.kind === "proxy" && (
        <form
          className="flex flex-col gap-1.5"
          onSubmit={(e) => {
            e.preventDefault()
            onSave({ proxy_target: target.trim() })
          }}
        >
          <Label htmlFor={`${id}-target`}>Pass requests to</Label>
          <div className="flex gap-2">
            <Input
              id={`${id}-target`}
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              placeholder="http://127.0.0.1:3000"
              disabled={proxyOff}
              autoComplete="off"
              spellCheck={false}
              aria-describedby={`${id}-target-help`}
            />
            <Button type="submit" variant="outline" disabled={proxyOff || pending || !targetChanged}>
              Save target
            </Button>
          </div>
          {proxyOff && proxy ? (
            <Detached feature={proxy} id={`${id}-target-help`} />
          ) : (
            <p id={`${id}-target-help`} className="text-muted-foreground text-xs">
              http or https, a host and a port, with no path.
            </p>
          )}
        </form>
      )}

      <ApplyError error={error} fallback="The change could not be saved." />

      <section aria-labelledby={`${id}-features`} className="flex flex-col gap-2">
        <h3 id={`${id}-features`} className="text-sm font-semibold">
          What the panel controls in this site's file
        </h3>
        <ul className="divide-border divide-y rounded-lg border">
          {detail.features.map((f) => (
            <li key={f.feature} className="flex flex-col gap-1 px-3 py-2" data-feature={f.feature}>
              <div className="flex items-center justify-between gap-3">
                <span className="text-sm font-medium">{featureLabel[f.feature]}</span>
                <Badge variant={f.attached ? "secondary" : "outline"}>
                  {f.attached ? "connected" : "detached"}
                </Badge>
              </div>
              <code className="text-muted-foreground text-xs break-all">include {f.include};</code>
              {!f.attached && f.reason && (
                <p className="text-muted-foreground text-xs">{f.reason}</p>
              )}
            </li>
          ))}
        </ul>
      </section>
    </div>
  )
}
