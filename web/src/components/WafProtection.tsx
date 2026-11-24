import { useId, useState, type FormEvent } from "react"

import { ErrorState } from "@/components/ErrorState"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { SkeletonRows } from "@/components/ui/skeleton"
import { Textarea } from "@/components/ui/textarea"
import { GLOBAL_LIMITS, isValidAgent, splitLines, type WafGlobal, type WafGlobalResponse } from "@/lib/waf"
import { useSaveWafGlobal, useWafGlobal } from "@/lib/wafQueries"

/**
 * The firewall rules that apply to every site: bot blocking, dotfile and TRACE
 * refusal, and an address deny list. A site's own firewall still applies on top.
 */
export function ProtectionPanel() {
  const global = useWafGlobal()
  if (global.isPending) return <SkeletonRows label="Loading firewall rules…" />
  if (global.error) {
    return (
      <ErrorState
        error={global.error}
        fallback="The global firewall rules are not available."
        onRetry={() => void global.refetch()}
      />
    )
  }
  // Keyed on the saved values, so the form starts again from what the server holds.
  return <ProtectionForm key={JSON.stringify(global.data.global)} data={global.data} />
}

function ProtectionForm({ data }: { data: WafGlobalResponse }) {
  const id = useId()
  const save = useSaveWafGlobal()
  const current = data.global
  const [botOn, setBotOn] = useState(current.bot_block.enabled)
  const [scanners, setScanners] = useState(current.bot_block.scanners)
  const [aiCrawlers, setAiCrawlers] = useState(current.bot_block.ai_crawlers)
  const [seoCrawlers, setSeoCrawlers] = useState(current.bot_block.seo_crawlers)
  const [emptyAgent, setEmptyAgent] = useState(current.bot_block.empty_agent)
  const [customText, setCustomText] = useState(current.bot_block.custom.join("\n"))
  const [sensitive, setSensitive] = useState(current.sensitive_files)
  const [trace, setTrace] = useState(current.block_trace)
  const [ipText, setIpText] = useState(current.ip_deny.join("\n"))

  const custom = splitLines(customText)
  const badAgents = custom.filter((a) => !isValidAgent(a))
  const ips = splitLines(ipText)
  const tooMany = custom.length > GLOBAL_LIMITS.customAgents

  function submit(e: FormEvent) {
    e.preventDefault()
    if (badAgents.length > 0 || tooMany) return
    const next: WafGlobal = {
      ...current,
      bot_block: {
        enabled: botOn,
        empty_agent: emptyAgent,
        scanners,
        ai_crawlers: aiCrawlers,
        seo_crawlers: seoCrawlers,
        custom,
      },
      sensitive_files: sensitive,
      block_trace: trace,
      ip_deny: ips,
    }
    save.mutate(next)
  }

  const presets = data.presets
  const agentCount =
    (scanners ? presets.scanners.length : 0) + (aiCrawlers ? presets.ai_crawlers.length : 0) + (seoCrawlers ? presets.seo_crawlers.length : 0)

  return (
    <form onSubmit={submit} className="flex flex-col gap-6" aria-label="Firewall rules for every site">
      <fieldset className="flex flex-col gap-3">
        <legend className="text-sm font-semibold">Bot blocking</legend>
        <p className="text-muted-foreground text-xs">
          Refuses requests whose User-Agent names a tool or crawler on the lists below. Browsers and ordinary clients
          are not affected.
        </p>
        <label className="flex items-center gap-2 text-sm font-medium">
          <input type="checkbox" checked={botOn} onChange={(e) => setBotOn(e.target.checked)} />
          Block bots by their User-Agent
        </label>

        <div className="flex flex-col gap-2 pl-6" hidden={!botOn}>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={scanners} onChange={(e) => setScanners(e.target.checked)} />
            Attack tools and scanners
            <span className="text-muted-foreground text-xs">({presets.scanners.length} names, for example sqlmap, nikto, nmap)</span>
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={aiCrawlers} onChange={(e) => setAiCrawlers(e.target.checked)} />
            AI training crawlers
            <span className="text-muted-foreground text-xs">({presets.ai_crawlers.length} names, for example GPTBot, ClaudeBot, CCBot)</span>
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={seoCrawlers} onChange={(e) => setSeoCrawlers(e.target.checked)} />
            SEO crawlers
            <span className="text-muted-foreground text-xs">({presets.seo_crawlers.length} names, for example AhrefsBot, SemrushBot)</span>
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={emptyAgent} onChange={(e) => setEmptyAgent(e.target.checked)} />
            Requests that send no User-Agent at all
          </label>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-custom`}>Your own User-Agent names, one per line</Label>
            <Textarea
              id={`${id}-custom`}
              rows={4}
              spellCheck={false}
              className="font-mono text-xs"
              value={customText}
              onChange={(e) => setCustomText(e.target.value)}
              aria-invalid={badAgents.length > 0 || tooMany}
              aria-describedby={`${id}-custom-hint`}
              placeholder={"MyScraper/1.0\nBadBot"}
            />
            <p id={`${id}-custom-hint`} className={badAgents.length || tooMany ? "text-destructive text-xs" : "text-muted-foreground text-xs"}>
              {tooMany
                ? `At most ${GLOBAL_LIMITS.customAgents} names.`
                : badAgents.length > 0
                  ? `Not allowed: ${badAgents.join(", ")}. Use letters, digits and . _ / + -, ${GLOBAL_LIMITS.agentLength} characters at most.`
                  : `${agentCount} preset names are on. A name matches any User-Agent that contains it, ignoring case.`}
            </p>
          </div>
        </div>
      </fieldset>

      <fieldset className="flex flex-col gap-3">
        <legend className="text-sm font-semibold">Files and methods</legend>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={sensitive} onChange={(e) => setSensitive(e.target.checked)} />
          Refuse dotfiles and dot-folders such as /.git, /.env and /.svn
          <span className="text-muted-foreground text-xs">(/.well-known/ stays open)</span>
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={trace} onChange={(e) => setTrace(e.target.checked)} />
          Refuse TRACE and TRACK requests
        </label>
      </fieldset>

      <fieldset className="flex flex-col gap-3">
        <legend className="text-sm font-semibold">Address deny list</legend>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-ips`}>Addresses and networks refused on every site, one per line</Label>
          <Textarea
            id={`${id}-ips`}
            rows={4}
            spellCheck={false}
            className="font-mono text-xs"
            value={ipText}
            onChange={(e) => setIpText(e.target.value)}
            placeholder={"203.0.113.7\n198.51.100.0/24"}
            aria-describedby={`${id}-ips-hint`}
          />
          <p id={`${id}-ips-hint`} className="text-muted-foreground text-xs">
            {ips.length} {ips.length === 1 ? "entry" : "entries"}. A site's own allow list does not override this list.
          </p>
        </div>
      </fieldset>

      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit" disabled={save.isPending || badAgents.length > 0 || tooMany}>
          {save.isPending ? "Applying…" : "Save firewall rules"}
        </Button>
        <p className="text-muted-foreground text-xs">
          Applied to every site with nginx, after `nginx -t` checks the result.
        </p>
      </div>

      {save.error && (
        <p role="alert" className="text-destructive text-sm">
          {save.error.message}
        </p>
      )}
      {save.data?.warnings?.map((w) => (
        <p key={w} className="text-muted-foreground text-sm">
          {w}
        </p>
      ))}
    </form>
  )
}
