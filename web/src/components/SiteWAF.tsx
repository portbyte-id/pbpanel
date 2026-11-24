import { Link } from "@tanstack/react-router"
import { X } from "lucide-react"
import { useId, useState } from "react"

import { AppsecWarning } from "@/components/AppsecWarning"
import { ApplyError, ApplyWarnings } from "@/components/ApplyError"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { ErrorState } from "@/components/ErrorState"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Callout } from "@/components/ui/callout"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select } from "@/components/ui/select"
import { SkeletonRows } from "@/components/ui/skeleton"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { TableFilter, TablePagination, TableToolbar, usePagedRows } from "@/components/ui/table-controls"
import type { AppsecAvailability } from "@/lib/appsec"
import { ApiError } from "@/lib/api"
import { matchesQuery } from "@/lib/table"
import {
  CLEARANCE_OPTIONS,
  CONFIDENCE_OPTIONS,
  configProblems,
  DEFAULT_WAF_CONFIG,
  DIFFICULTY_OPTIONS,
  ENGINE_LABEL,
  EXCEPTION_KIND_LABEL,
  EXCEPTION_KINDS,
  EXCLUSION_KIND_LABEL,
  exceptionProblem,
  exclusionProblem,
  engineOf,
  isValidArg,
  isValidIPEntry,
  isValidPath,
  MODE_LABEL,
  OPENAPPSEC_MODE_LABEL,
  PARANOIA_HELP,
  splitList,
  WAF_LIMITS,
  type ChallengeProvider,
  type WafChallenge,
  type WafConfig,
  type WafConfidence,
  type WafEngine,
  type WafExclusion,
  type WafExclusionKind,
  type WafExceptionKind,
  type WafMode,
  type WafOpenAppsecException,
  type WafSiteStatus,
} from "@/lib/waf"
import { useSaveSiteWaf, useSiteWaf } from "@/lib/wafQueries"

/** A rule to exclude, handed over from the audit viewer through the URL. */
export interface WafPrefill {
  rule?: number
  path?: string
}

/** The tab each whole-config problem is shown on, so Save can say where to look. */
const PROBLEM_TAB: Record<string, string> = {
  requests_per_second: "Rate limit and paths",
  burst: "Rate limit and paths",
  path_blocklist: "Rate limit and paths",
  ip_allow: "IP rules",
  ip_deny: "IP rules",
  exclusions: "Exclusions",
  open_appsec_confidence: "Mode and rules",
  open_appsec_exceptions: "Exclusions",
  exempt_paths: "Challenge",
  exempt_ips: "Challenge",
}

/**
 * The firewall of one site: the engine that inspects its requests (ModSecurity
 * with the OWASP Core Rule Set, or open-appsec), and the native nginx
 * protections. One Save writes the whole configuration, and nginx checks it
 * (`nginx -t`) before anything is applied.
 */
export function SiteWAF({ domain, prefill }: { domain: string; prefill?: WafPrefill }) {
  const { data, isPending, error, refetch } = useSiteWaf(domain)

  if (isPending) return <SkeletonRows label="Loading the firewall settings…" />
  if (error) {
    return (
      <ErrorState
        error={error}
        fallback="The firewall settings could not be loaded."
        onRetry={() => void refetch()}
      />
    )
  }
  // A panel older than the challenge page sends no challenge; fill in the defaults.
  // The engine and its settings are filled the same way, so a row stored before
  // they existed reads as ModSecurity with the default confidence.
  const saved: WafConfig = {
    ...data.config,
    engine: engineOf(data.config),
    open_appsec: { ...DEFAULT_WAF_CONFIG.open_appsec, ...data.config.open_appsec },
    challenge: { ...DEFAULT_WAF_CONFIG.challenge, ...data.config.challenge },
  }
  // Keyed on what the server holds, so a save or a reload resets the form to it.
  return (
    <WafForm
      key={JSON.stringify(saved)}
      domain={domain}
      saved={saved}
      status={data.status}
      appsec={data.open_appsec}
      prefill={prefill}
    />
  )
}

function WafForm({
  domain,
  saved,
  status,
  appsec,
  prefill,
}: {
  domain: string
  saved: WafConfig
  status: WafSiteStatus
  /** The second engine's availability and its tracking warning; absent on a panel without it. */
  appsec?: AppsecAvailability
  prefill?: WafPrefill
}) {
  const id = useId()
  const save = useSaveSiteWaf(domain)
  const [draft, setDraft] = useState<WafConfig>(saved)
  const [confirming, setConfirming] = useState(false)

  const dirty = JSON.stringify(draft) !== JSON.stringify(saved)
  const problems = configProblems(draft)
  const problemList = Object.values(problems)
  const problemTabs = [...new Set(Object.keys(problems).map((k) => PROBLEM_TAB[k]).filter(Boolean))]
  const set = (patch: Partial<WafConfig>) => {
    save.reset()
    setDraft((d) => ({ ...d, ...patch }))
  }

  const submit = () => {
    save.mutate(draft, { onSuccess: () => setConfirming(false) })
  }
  const onSave = () => {
    // Switching to blocking is the one change that starts refusing live traffic,
    // whichever engine does it, so it is the one that asks first.
    if (draft.mode === "block" && saved.mode !== "block") setConfirming(true)
    else submit()
  }

  const modsecOff = !status.available
  const rl = draft.rate_limit
  const engine = engineOf(draft)
  // An engine the host cannot run right now: disabled in the picker, with the
  // reason the API gave. The stored engine still renders its settings, so a site
  // left on an engine whose agent was removed can be switched back.
  const openAppsecReady = appsec?.available === true
  const openAppsecBlocked = !openAppsecReady && engine !== "open-appsec"
  const openAppsecReason =
    appsec?.reason || "open-appsec is not available on this host. Install its agent from the Store."
  // Whether the engine the form is *on* can inspect at all. A site left on
  // open-appsec after its agent was removed cannot be saved into an inspecting
  // mode — the server refuses it — so the modes say so instead of failing on Save.
  const engineUsable = engine === "modsecurity" ? !modsecOff : openAppsecReady

  return (
    <form
      className="flex flex-col gap-8"
      onSubmit={(e) => {
        e.preventDefault()
        if (dirty && problemList.length === 0) onSave()
      }}
      aria-label={`Firewall for ${domain}`}
    >
      <EnginePicker
        id={id}
        engine={engine}
        appsec={appsec}
        openAppsecBlocked={openAppsecBlocked}
        openAppsecReason={openAppsecReason}
        onChange={(next) => set({ engine: next })}
      />

      <Tabs defaultValue={prefill?.rule !== undefined ? "exclusions" : "mode"}>
        <TabsList aria-label={`Firewall sections for ${domain}`} className="flex-wrap">
          <TabsTrigger value="mode">Mode and rules</TabsTrigger>
          <TabsTrigger value="rate">Rate limit and paths</TabsTrigger>
          <TabsTrigger value="ip">IP rules</TabsTrigger>
          <TabsTrigger value="exclusions">Exclusions</TabsTrigger>
          <TabsTrigger value="challenge">Challenge</TabsTrigger>
        </TabsList>

        {/* Panels stay mounted while another tab is shown, so an entry typed in one
            tab is still there when the operator comes back to it. */}

        {/* --- The selected engine's mode and settings ------------------------- */}
        <TabsContent value="mode" forceMount className="pt-2">
          <section aria-labelledby={`${id}-engine-settings`} className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center gap-2">
              <h3 id={`${id}-engine-settings`} className="text-sm font-semibold">
                {ENGINE_LABEL[engine]}
              </h3>
              {engine === "modsecurity" && status.available && (
                <Badge variant="secondary">OWASP CRS{status.crs_version ? ` ${status.crs_version}` : ""}</Badge>
              )}
              {engine === "open-appsec" && appsec?.agent_version && (
                <Badge variant="secondary">agent {appsec.agent_version}</Badge>
              )}
            </div>

            {engine === "modsecurity" && modsecOff && (
              <Callout tone="warning" role="status" className="text-sm">
                <p className="font-medium">ModSecurity is not available on this server.</p>
                <p className="text-muted-foreground mt-1">
                  {status.reason ||
                    "It is built into the post-quantum nginx flavor, together with the OWASP Core Rule Set."}{" "}
                  Switch the web server to that flavor on the{" "}
                  <Link to="/runtimes" className="underline underline-offset-4">
                    Runtimes page
                  </Link>{" "}
                  to turn it on. The native protections below work with any flavor.
                </p>
              </Callout>
            )}

            <fieldset className="flex flex-col gap-1.5">
              <legend className="text-sm font-medium">Mode</legend>
              <div role="radiogroup" aria-label={`${ENGINE_LABEL[engine]} mode`} className="inline-flex flex-wrap gap-1">
                {(["off", "detect", "block"] as WafMode[]).map((m) => {
                  const unavailable = !engineUsable && m !== "off"
                  return (
                    <label
                      key={m}
                      className="has-[:checked]:bg-primary has-[:checked]:text-primary-foreground has-[:focus-visible]:ring-ring/50 has-[:disabled]:opacity-50 border-input cursor-pointer rounded-md border px-3 py-1.5 text-sm has-[:disabled]:cursor-not-allowed has-[:focus-visible]:ring-[3px]"
                    >
                      <input
                        type="radio"
                        name={`${id}-mode`}
                        value={m}
                        checked={draft.mode === m}
                        disabled={unavailable}
                        onChange={() => set({ mode: m })}
                        className="sr-only"
                      />
                      {MODE_LABEL[m]}
                    </label>
                  )
                })}
              </div>
              <p className="text-muted-foreground text-xs">
                {draft.mode === "off" && `Requests are not inspected by ${ENGINE_LABEL[engine]}.`}
                {draft.mode === "detect" &&
                  (engine === "open-appsec"
                    ? "The agent inspects requests and logs what it finds, but nothing is blocked. open-appsec calls this detect-learn: it also learns from the traffic to reduce false positives later."
                    : "Requests are inspected and matches are logged to the audit log, but nothing is blocked. Use this first to find false positives.")}
                {draft.mode === "block" &&
                  (engine === "open-appsec"
                    ? "Requests the agent scores at or above the confidence below are refused with 403, and logged. open-appsec calls this prevent-learn."
                    : "Requests that score over the threshold are refused with 403, and logged.")}
              </p>
              {engine === "open-appsec" && draft.mode !== "off" && (
                <p className="text-muted-foreground text-xs" data-testid="appsec-mode-name">
                  In the agent&apos;s own words this mode is <code>{OPENAPPSEC_MODE_LABEL[draft.mode]}</code>.
                </p>
              )}
              {engine === "open-appsec" && !openAppsecReady && (
                <p className="text-muted-foreground text-xs" role="status">
                  This site is set to open-appsec, but {openAppsecReason} Until it is back, only Off can be saved;
                  switching to ModSecurity above is the other way out.
                </p>
              )}
            </fieldset>

            {engine === "modsecurity" ? (
              <div className="flex w-full flex-col gap-1.5">
                <Label htmlFor={`${id}-paranoia`}>Paranoia level</Label>
                <Select
                  id={`${id}-paranoia`}
                  value={draft.paranoia}
                  disabled={draft.mode === "off" || modsecOff}
                  onChange={(e) => set({ paranoia: Number(e.target.value) as WafConfig["paranoia"] })}
                  aria-describedby={`${id}-paranoia-help`}
                >
                  {([1, 2, 3, 4] as const).map((n) => (
                    <option key={n} value={n}>
                      {n}
                    </option>
                  ))}
                </Select>
                <p id={`${id}-paranoia-help`} className="text-muted-foreground text-xs">
                  {PARANOIA_HELP[draft.paranoia]}
                </p>
              </div>
            ) : (
              <div className="flex w-full flex-col gap-1.5">
                <Label htmlFor={`${id}-confidence`}>Minimum confidence to block</Label>
                <Select
                  id={`${id}-confidence`}
                  value={draft.open_appsec.confidence}
                  disabled={draft.mode !== "block" || openAppsecBlocked}
                  aria-invalid={problems.open_appsec_confidence !== undefined}
                  aria-describedby={`${id}-confidence-help`}
                  onChange={(e) =>
                    set({
                      open_appsec: { ...draft.open_appsec, confidence: e.target.value as WafConfidence },
                    })
                  }
                >
                  {CONFIDENCE_OPTIONS.map((o) => (
                    <option key={o.value} value={o.value}>
                      {o.label}
                    </option>
                  ))}
                </Select>
                <p id={`${id}-confidence-help`} className="text-muted-foreground text-xs">
                  {CONFIDENCE_OPTIONS.find((o) => o.value === draft.open_appsec.confidence)?.help ??
                    "Choose critical, high or medium."}{" "}
                  {draft.mode !== "block" && "It applies only in blocking mode."}
                </p>
              </div>
            )}

            {!status.attached && (
              <p className="text-muted-foreground text-xs">
                The site file has no firewall block yet. Saving adds one; the previous version of the file
                stays in its history.
              </p>
            )}
          </section>
        </TabsContent>

        {/* --- Challenge page ------------------------------------------------- */}
        <TabsContent value="challenge" forceMount className="pt-2">
          <ChallengeSection
            domain={domain}
            challenge={draft.challenge}
            available={status.challenge_available !== false}
            turnstileReady={status.turnstile_ready}
            onChange={(challenge) => set({ challenge })}
          />
        </TabsContent>

        {/* --- Native protections: rate limit and paths ----------------------- */}
        <TabsContent value="rate" forceMount className="pt-2">
          <section aria-labelledby={`${id}-native`} className="flex flex-col gap-5">
            <div className="flex flex-col gap-0.5">
              <h3 id={`${id}-native`} className="text-sm font-semibold">
                Native nginx protections
              </h3>
              <p className="text-muted-foreground text-xs">
                Evaluated by nginx itself, before the request reaches your site. They work whichever engine inspects.
              </p>
            </div>

            <div className="flex flex-col gap-3">
              <label className="flex items-center gap-2 text-sm font-medium">
                <input
                  type="checkbox"
                  checked={rl.enabled}
                  onChange={(e) => set({ rate_limit: { ...rl, enabled: e.target.checked } })}
                />
                Rate limit each client IP
              </label>
              <div className="flex flex-wrap gap-4">
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor={`${id}-rps`}>Requests per second</Label>
                  <Input
                    id={`${id}-rps`}
                    type="number"
                    min={1}
                    max={10000}
                    inputMode="numeric"
                    className="w-36"
                    disabled={!rl.enabled}
                    value={Number.isNaN(rl.requests_per_second) ? "" : rl.requests_per_second}
                    aria-invalid={rl.enabled && problems.requests_per_second !== undefined}
                    onChange={(e) =>
                      set({
                        rate_limit: {
                          ...rl,
                          requests_per_second: e.target.value === "" ? NaN : Number(e.target.value),
                        },
                      })
                    }
                  />
                </div>
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor={`${id}-burst`}>Burst</Label>
                  <Input
                    id={`${id}-burst`}
                    type="number"
                    min={0}
                    max={100000}
                    inputMode="numeric"
                    className="w-36"
                    disabled={!rl.enabled}
                    value={Number.isNaN(rl.burst) ? "" : rl.burst}
                    aria-invalid={rl.enabled && problems.burst !== undefined}
                    onChange={(e) =>
                      set({
                        rate_limit: { ...rl, burst: e.target.value === "" ? NaN : Number(e.target.value) },
                      })
                    }
                  />
                </div>
              </div>
              <p className="text-muted-foreground text-xs">
                Requests above the rate, beyond the burst, are refused with 429. A burst lets a page load
                its images in one go.
              </p>
              {rl.enabled && problems.requests_per_second && (
                <p role="alert" className="text-destructive text-xs">
                  {problems.requests_per_second}
                </p>
              )}
              {rl.enabled && problems.burst && (
                <p role="alert" className="text-destructive text-xs">
                  {problems.burst}
                </p>
              )}
            </div>

            <ListEditor
              label="Blocked paths"
              help="A request whose path starts with one of these gets 403. For example /wp-admin or /.git."
              placeholder="/wp-admin"
              items={draft.path_blocklist}
              max={WAF_LIMITS.paths}
              validate={(p) =>
                isValidPath(p)
                  ? null
                  : "Start with / and use only letters, digits and / _ . ~ % -."
              }
              onChange={(items) => set({ path_blocklist: items })}
            />
          </section>
        </TabsContent>

        {/* --- Native protections: IP rules ----------------------------------- */}
        <TabsContent value="ip" forceMount className="pt-2">
          <section aria-labelledby={`${id}-ip`} className="flex flex-col gap-5">
            <div className="flex flex-col gap-0.5">
              <h3 id={`${id}-ip`} className="text-sm font-semibold">
                IP rules
              </h3>
              <p className="text-muted-foreground text-xs">
                Addresses and networks nginx always admits or always refuses, before ModSecurity looks at the request.
              </p>
            </div>

            <div className="grid gap-5 md:grid-cols-2">
              <ListEditor
                label="Always allow these IPs"
                help="Addresses or CIDR ranges. When this list is not empty, every other address gets 403."
                placeholder="203.0.113.0/24"
                items={draft.ip_rules.allow}
                max={WAF_LIMITS.ips}
                validate={(v) => (isValidIPEntry(v) ? null : "Not an IP address or CIDR range.")}
                onChange={(items) => set({ ip_rules: { ...draft.ip_rules, allow: items } })}
              />
              <ListEditor
                label="Always block these IPs"
                help="Addresses or CIDR ranges that get 403. A block wins over an allow."
                placeholder="198.51.100.7"
                items={draft.ip_rules.deny}
                max={WAF_LIMITS.ips}
                validate={(v) => (isValidIPEntry(v) ? null : "Not an IP address or CIDR range.")}
                onChange={(items) => set({ ip_rules: { ...draft.ip_rules, deny: items } })}
              />
            </div>
            {draft.ip_rules.allow.length > 0 && (
              <Callout tone="warning" role="status" className="text-sm">
                With an allow list, only those addresses can reach this site, certificate challenges for HTTPS
                excepted. Make sure your own address is on it.
              </Callout>
            )}
          </section>
        </TabsContent>

        {/* --- Exclusions ----------------------------------------------------- */}
        <TabsContent value="exclusions" forceMount className="pt-2">
          {engine === "open-appsec" ? (
            <ExceptionEditor
              exceptions={draft.open_appsec.exceptions}
              disabled={openAppsecBlocked && draft.open_appsec.exceptions.length === 0}
              onChange={(exceptions) => set({ open_appsec: { ...draft.open_appsec, exceptions } })}
            />
          ) : (
            <ExclusionEditor
              exclusions={draft.exclusions}
              disabled={modsecOff && draft.exclusions.length === 0}
              prefill={prefill}
              onChange={(exclusions) => set({ exclusions })}
            />
          )}
        </TabsContent>
      </Tabs>

      {/* --- Save ------------------------------------------------------------ */}
      <div className="bg-background/95 sticky bottom-0 -mx-4 flex flex-col gap-3 border-t px-4 py-3 backdrop-blur-sm">
        <ApplyError error={save.error} fallback="The firewall settings could not be saved." />
        {problemList.length > 0 && (
          <div role="alert" className="text-destructive flex flex-col gap-0.5 text-xs">
            <p>
              Fix the highlighted values to save
              {problemTabs.length > 0 ? `. They are on: ${problemTabs.join(", ")}.` : "."}
            </p>
            {/* The sentence itself, not only the tab: a disabled Save with no
                reason is the thing this form is meant not to do. */}
            <ul className="flex list-disc flex-col gap-0.5 pl-4">
              {problemList.map((p) => (
                <li key={p}>{p}</li>
              ))}
            </ul>
          </div>
        )}
        <div className="flex flex-wrap items-center gap-3">
          <Button type="submit" disabled={!dirty || problemList.length > 0 || save.isPending}>
            {save.isPending ? "Checking and applying…" : "Save and apply"}
          </Button>
          <Button
            type="button"
            variant="outline"
            disabled={!dirty || save.isPending}
            onClick={() => {
              save.reset()
              setDraft(saved)
            }}
          >
            Discard changes
          </Button>
          <span className="text-muted-foreground text-xs" aria-live="polite">
            {save.isSuccess && !dirty ? "Saved. nginx accepted the configuration." : ""}
          </span>
        </div>
        <ApplyWarnings warnings={save.data?.warnings} />
      </div>

      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={`Block attacks on ${domain}?`}
        confirmLabel="Switch to blocking"
        pendingLabel="Applying…"
        pending={save.isPending}
        error={save.error instanceof ApiError ? save.error.detail : null}
        onConfirm={submit}
      >
        <p>
          Blocking mode affects live traffic: a request that {ENGINE_LABEL[engine]} scores as an attack gets a 403
          straight away, including a legitimate one it misjudges.
        </p>
        <p>
          If you have not run Detection only first, do that and review the audit log for false positives
          before blocking.
        </p>
      </ConfirmDialog>
    </form>
  )
}

/**
 * The engine picker: which engine inspects this site's requests.
 *
 * open-appsec is never offered without its tracking statement. The D5 decision
 * (docs/design/open-appsec-plan.md §6.1) is that using this engine can subject
 * the host to Check Point's tracking, and the warning is rendered inline, under
 * the option itself, from the API's own `warning` string — so the choice cannot
 * be made without seeing it. An engine the host cannot run is disabled, says
 * why (the API's `reason`), and links to the Store to install what is missing.
 */
function EnginePicker({
  id,
  engine,
  appsec,
  openAppsecBlocked,
  openAppsecReason,
  onChange,
}: {
  id: string
  engine: WafEngine
  /** Absent on a panel with no appsec package at all. */
  appsec?: AppsecAvailability
  openAppsecBlocked: boolean
  openAppsecReason: string
  onChange: (next: WafEngine) => void
}) {
  const engines: WafEngine[] = ["modsecurity", "open-appsec"]
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="text-sm font-semibold">Inspection engine</legend>
      <p className="text-muted-foreground text-xs">
        What reads this site&apos;s requests. The native protections below and the challenge page apply whichever
        engine is chosen.
      </p>
      <div className="flex flex-col gap-2">
        {engines.map((e) => {
          const disabled = e === "open-appsec" && openAppsecBlocked
          const chosen = engine === e
          return (
            <div
              key={e}
              className={
                "border-input flex flex-col gap-2 rounded-md border p-3 " +
                (chosen ? "border-primary/60 bg-primary/5" : "")
              }
            >
              <label
                className={
                  "flex items-start gap-2 " + (disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer")
                }
              >
                <input
                  type="radio"
                  name={`${id}-engine`}
                  value={e}
                  checked={chosen}
                  disabled={disabled}
                  aria-describedby={e === "open-appsec" ? `${id}-appsec-state` : undefined}
                  onChange={() => onChange(e)}
                  className="mt-0.5"
                />
                <span className="flex min-w-0 flex-col gap-0.5">
                  <span className="text-sm font-medium">{ENGINE_LABEL[e]}</span>
                  <span className="text-muted-foreground text-xs">
                    {e === "modsecurity"
                      ? "The OWASP Core Rule Set: a large, published rule set with paranoia levels and per-rule exclusions."
                      : "Check Point's machine-learning engine. It holds its own connection to Check Point's cloud; see the statement below."}
                  </span>
                </span>
                {disabled && (
                  <Badge variant="outline" className="ml-auto shrink-0">
                    unavailable
                  </Badge>
                )}
              </label>

              {e === "open-appsec" && (
                <>
                  {/* The tracking statement, from the API, inline: the choice is
                      never made without it being on screen. */}
                  {appsec?.warning ? (
                    <AppsecWarning warning={appsec.warning} compact className="ml-6" />
                  ) : (
                    <p className="text-muted-foreground ml-6 text-xs">
                      The engine&apos;s tracking statement is not available from this panel, so it cannot be offered
                      here.
                    </p>
                  )}
                  <p id={`${id}-appsec-state`} className="ml-6 text-xs">
                    {openAppsecBlocked ? (
                      <span className="text-muted-foreground">
                        {openAppsecReason}{" "}
                        <Link to="/store" className="underline underline-offset-4">
                          Open the Store
                        </Link>{" "}
                        to install it.
                      </span>
                    ) : (
                      <span className="text-muted-foreground">
                        Ready on this host{appsec?.agent_version ? ` (agent ${appsec.agent_version})` : ""}.
                      </span>
                    )}
                  </p>
                </>
              )}
            </div>
          )
        })}
      </div>
    </fieldset>
  )
}

/**
 * The open-appsec exceptions in force, and the form that adds one.
 *
 * A **path** exception is not offered and cannot be added. Step 0 and step 4 both
 * measured that the agent's `urlPath` exception does not compile
 * (docs/design/open-appsec-step4-findings.md §C), so a control for it would do
 * nothing while looking like it did. A stored row that still carries the kind is
 * listed with that reason and can only be removed — the UI never pretends the
 * feature works.
 */
function ExceptionEditor({
  exceptions,
  disabled,
  onChange,
}: {
  exceptions: WafOpenAppsecException[]
  disabled: boolean
  onChange: (next: WafOpenAppsecException[]) => void
}) {
  const id = useId()
  const [kind, setKind] = useState<WafExceptionKind>("source_ip")
  const [sourceIP, setSourceIP] = useState("")
  const [param, setParam] = useState("")
  const [note, setNote] = useState("")
  const [problem, setProblem] = useState<string | null>(null)

  const add = () => {
    const entry: WafOpenAppsecException = {
      kind,
      path: "",
      source_ip: kind === "source_ip" ? sourceIP.trim() : "",
      param: kind === "param" ? param.trim() : "",
      note: note.trim(),
    }
    const bad = exceptionProblem(entry)
    if (bad) {
      setProblem(bad)
      return
    }
    if (exceptions.length >= WAF_LIMITS.exclusions) {
      setProblem(`At most ${WAF_LIMITS.exclusions} exceptions.`)
      return
    }
    setProblem(null)
    onChange([...exceptions, entry])
    setSourceIP("")
    setParam("")
    setNote("")
  }

  return (
    <section aria-labelledby={`${id}-heading`} className="flex flex-col gap-3">
      <div className="flex flex-col gap-0.5">
        <h3 id={`${id}-heading`} className="text-sm font-semibold">
          open-appsec exceptions
        </h3>
        <p className="text-muted-foreground text-xs">
          Stop the agent from inspecting a source network or one request parameter, where it misjudges your
          application.
        </p>
      </div>

      {/* Said plainly, once, rather than as a disabled control nobody can explain. */}
      <p className="text-muted-foreground text-xs">
        Path exceptions are not offered: the agent does not compile them, so one would silently match nothing.
        Use a source network or a request parameter instead.
      </p>

      {exceptions.length === 0 ? (
        <p className="text-muted-foreground text-sm">No exceptions.</p>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-sm">
            <caption className="sr-only">open-appsec exceptions</caption>
            <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
              <tr>
                <th scope="col" className="px-3 py-1.5 font-medium">Exception</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Where</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Note</th>
                <th scope="col" className="px-3 py-1.5 font-medium">
                  <span className="sr-only">Remove</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {exceptions.map((e, i) => (
                <tr key={`${e.kind}:${e.source_ip}:${e.param}:${e.path}:${i}`}>
                  <td className="px-3 py-1.5">
                    {EXCEPTION_KIND_LABEL[e.kind]}
                    {e.kind === "path" && (
                      <Badge variant="warning" className="ml-2">
                        not supported
                      </Badge>
                    )}
                  </td>
                  <td className="px-3 py-1.5 font-mono text-xs break-all">
                    {e.kind === "path" && <span className="text-muted-foreground font-sans">does not apply</span>}
                    {e.kind === "source_ip" && e.source_ip}
                    {e.kind === "param" && `arg ${e.param}`}
                  </td>
                  <td className="text-muted-foreground px-3 py-1.5">{e.note || "—"}</td>
                  <td className="px-3 py-1.5 text-right">
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      aria-label={`Remove ${EXCEPTION_KIND_LABEL[e.kind]} exception`}
                      onClick={() => onChange(exceptions.filter((_, j) => j !== i))}
                    >
                      Remove
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <fieldset
        disabled={disabled}
        className="grid gap-3 rounded-lg border p-3 sm:grid-cols-2"
        aria-label="Add an open-appsec exception"
      >
        <div className="flex flex-col gap-1.5 sm:col-span-2">
          <Label htmlFor={`${id}-kind`}>Exception type</Label>
          <Select id={`${id}-kind`} value={kind} onChange={(e) => setKind(e.target.value as WafExceptionKind)}>
            {EXCEPTION_KINDS.map((k) => (
              <option key={k} value={k}>
                {EXCEPTION_KIND_LABEL[k]}
              </option>
            ))}
          </Select>
        </div>
        {kind === "source_ip" && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-source`}>Source network</Label>
            <Input
              id={`${id}-source`}
              value={sourceIP}
              placeholder="203.0.113.0/24"
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
              aria-invalid={sourceIP !== "" && !isValidIPEntry(sourceIP)}
              onChange={(e) => setSourceIP(e.target.value)}
            />
          </div>
        )}
        {kind === "param" && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-param`}>Parameter name</Label>
            <Input
              id={`${id}-param`}
              value={param}
              placeholder="comment"
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
              aria-invalid={param !== "" && !isValidArg(param)}
              onChange={(e) => setParam(e.target.value)}
            />
          </div>
        )}
        <div className="flex flex-col gap-1.5 sm:col-span-2">
          <Label htmlFor={`${id}-note`}>Note (optional)</Label>
          <Input
            id={`${id}-note`}
            value={note}
            maxLength={WAF_LIMITS.note}
            placeholder="Why this is safe"
            autoComplete="off"
            onChange={(e) => setNote(e.target.value)}
          />
        </div>
        {problem && (
          <p role="alert" className="text-destructive text-xs sm:col-span-2">
            {problem}
          </p>
        )}
        <div className="sm:col-span-2">
          <Button type="button" variant="outline" onClick={add}>
            Add exception
          </Button>
        </div>
      </fieldset>
    </section>
  )
}

const PROVIDER_LABEL: Record<ChallengeProvider, string> = {
  pow: "Proof of work",
  turnstile: "Cloudflare Turnstile",
}

/**
 * The page a visitor passes before the site opens. nginx checks the clearance
 * cookie on its own, so a visitor who passed costs nothing more.
 */
function ChallengeSection({
  domain,
  challenge: ch,
  available,
  turnstileReady,
  onChange,
}: {
  domain: string
  challenge: WafChallenge
  available: boolean
  /** Whether Settings holds both Turnstile keys; undefined from an older panel. */
  turnstileReady?: boolean
  onChange: (next: WafChallenge) => void
}) {
  const id = useId()
  const set = (patch: Partial<WafChallenge>) => onChange({ ...ch, ...patch })
  const off = !ch.enabled
  const difficultyKnown = DIFFICULTY_OPTIONS.some((o) => o.value === ch.difficulty)
  const clearanceKnown = CLEARANCE_OPTIONS.some((o) => o.value === ch.clearance_minutes)

  return (
    <section aria-labelledby={`${id}-heading`} className="flex flex-col gap-4">
      <div className="flex flex-col gap-0.5">
        <h3 id={`${id}-heading`} className="text-sm font-semibold">
          Challenge page
        </h3>
        <p className="text-muted-foreground text-xs">
          Visitors see a short check before {domain} opens. Once they pass, nginx lets them through on its
          own until the clearance runs out.
        </p>
      </div>

      {!available && (
        <Callout tone="warning" role="status" className="text-sm">
          The challenge page needs the panel's secret key (data/secret.key), which this panel does not
          have. Reinstalling the panel creates it.
        </Callout>
      )}

      <label className="flex items-center gap-2 text-sm font-medium">
        <input
          type="checkbox"
          checked={ch.enabled}
          disabled={!available && !ch.enabled}
          onChange={(e) => set({ enabled: e.target.checked })}
        />
        Check visitors before the site opens
      </label>

      <fieldset className="flex flex-col gap-1.5" disabled={off}>
        <legend className="text-sm font-medium">Check</legend>
        <div role="radiogroup" aria-label="Challenge type" className="inline-flex flex-wrap gap-1">
          {(["pow", "turnstile"] as ChallengeProvider[]).map((p) => (
            <label
              key={p}
              className="has-[:checked]:bg-primary has-[:checked]:text-primary-foreground has-[:focus-visible]:ring-ring/50 has-[:disabled]:opacity-50 border-input cursor-pointer rounded-md border px-3 py-1.5 text-sm has-[:disabled]:cursor-not-allowed has-[:focus-visible]:ring-[3px]"
            >
              <input
                type="radio"
                name={`${id}-provider`}
                value={p}
                checked={ch.provider === p}
                onChange={() => set({ provider: p })}
                className="sr-only"
              />
              {PROVIDER_LABEL[p]}
            </label>
          ))}
        </div>
        <p className="text-muted-foreground text-xs">
          {ch.provider === "pow"
            ? "The browser solves a small puzzle in JavaScript, with no interaction. Nothing leaves this server. It slows bots down; it does not tell a person from a headless browser."
            : "Cloudflare Turnstile decides whether the visitor is a person, usually without a click. The visitor's browser talks to Cloudflare; the site does not have to be on Cloudflare."}
        </p>
      </fieldset>

      {ch.provider === "turnstile" && (
        <div className="flex flex-col gap-3">
          <p className="text-muted-foreground text-xs">
            The Turnstile site key and secret key are shared by every site. Set them under{" "}
            <Link to="/settings" className="underline underline-offset-4">
              Settings
            </Link>
            , in the Firewall group.
          </p>
          {turnstileReady === false && (
            <Callout tone="warning" role="status" className="text-sm">
              Turnstile is not set up on this panel yet. Until the site key and secret key are both set under
              Settings, every visitor to {domain} is stopped at a check that cannot load.
            </Callout>
          )}
        </div>
      )}

      <div className="flex flex-wrap gap-4">
        {ch.provider === "pow" && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-difficulty`}>Puzzle difficulty</Label>
            <Select
              id={`${id}-difficulty`}
              value={ch.difficulty}
              disabled={off}
              onChange={(e) => set({ difficulty: Number(e.target.value) })}
            >
              {!difficultyKnown && <option value={ch.difficulty}>Custom ({ch.difficulty})</option>}
              {DIFFICULTY_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          </div>
        )}
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-clearance`}>Let a visitor through for</Label>
          <Select
            id={`${id}-clearance`}
            value={ch.clearance_minutes}
            disabled={off}
            onChange={(e) => set({ clearance_minutes: Number(e.target.value) })}
          >
            {!clearanceKnown && <option value={ch.clearance_minutes}>{ch.clearance_minutes} minutes</option>}
            {CLEARANCE_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </Select>
        </div>
      </div>

      <div className="grid gap-5 md:grid-cols-2">
        <ListEditor
          label="Never check these paths"
          help="Path prefixes programs call, not browsers: APIs, webhooks, feeds. They cannot pass a check."
          placeholder="/api/"
          items={ch.exempt_paths}
          max={WAF_LIMITS.paths}
          validate={(p) =>
            p === "/"
              ? "That is the whole site; turn the check off instead."
              : isValidPath(p)
                ? null
                : "Start with / and use only letters, digits and / _ . ~ % -."
          }
          onChange={(items) => set({ exempt_paths: items })}
        />
        <ListEditor
          label="Never check these IPs"
          help="Addresses or CIDR ranges, such as your uptime monitor or office."
          placeholder="203.0.113.0/24"
          items={ch.exempt_ips}
          max={WAF_LIMITS.ips}
          validate={(v) => (isValidIPEntry(v) ? null : "Not an IP address or CIDR range.")}
          onChange={(items) => set({ exempt_ips: items })}
        />
      </div>

      {ch.enabled && (
        <Callout tone="info" role="status" className="text-sm">
          Search engines and link previews cannot pass the check either, so pages behind it drop out of
          search results while it is on. Certificate renewal is never checked.
        </Callout>
      )}
    </section>
  )
}

/** A list of short strings with an add box. Pasting several separated by space or comma adds each. */
function ListEditor({
  label,
  help,
  placeholder,
  items,
  max,
  validate,
  onChange,
}: {
  label: string
  help: string
  placeholder: string
  items: string[]
  max: number
  validate: (item: string) => string | null
  onChange: (items: string[]) => void
}) {
  const id = useId()
  const [text, setText] = useState("")
  const [problem, setProblem] = useState<string | null>(null)

  const add = () => {
    const entries = splitList(text)
    if (entries.length === 0) return
    for (const e of entries) {
      const bad = validate(e)
      if (bad) {
        setProblem(`${e}: ${bad}`)
        return
      }
    }
    const next = [...items, ...entries.filter((e) => !items.includes(e))]
    if (next.length > max) {
      setProblem(`At most ${max} entries.`)
      return
    }
    setProblem(null)
    setText("")
    onChange(next)
  }

  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={`${id}-add`}>{label}</Label>
      <div className="flex gap-2">
        <Input
          id={`${id}-add`}
          value={text}
          placeholder={placeholder}
          autoComplete="off"
          spellCheck={false}
          className="font-mono"
          aria-invalid={problem !== null}
          aria-describedby={`${id}-help`}
          onChange={(e) => {
            setText(e.target.value)
            setProblem(null)
          }}
          onKeyDown={(e) => {
            // Enter adds the entry instead of submitting the whole form.
            if (e.key === "Enter") {
              e.preventDefault()
              add()
            }
          }}
        />
        <Button type="button" variant="outline" onClick={add} disabled={text.trim() === ""}>
          Add
        </Button>
      </div>
      {problem && (
        <p role="alert" className="text-destructive text-xs">
          {problem}
        </p>
      )}
      <p id={`${id}-help`} className="text-muted-foreground text-xs">
        {help}
      </p>
      {items.length > 0 && (
        <ul aria-label={label} className="flex flex-wrap gap-1.5">
          {items.map((item) => (
            <li
              key={item}
              className="bg-muted flex items-center gap-1 rounded-md py-0.5 pr-0.5 pl-2 font-mono text-xs"
            >
              <span className="break-all">{item}</span>
              <button
                type="button"
                aria-label={`Remove ${item}`}
                onClick={() => onChange(items.filter((i) => i !== item))}
                className="hover:bg-accent focus-visible:ring-ring/50 rounded p-1 outline-none focus-visible:ring-[3px]"
              >
                <X className="size-3" aria-hidden="true" />
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/** The exclusions in force, with search, a type filter and paging. Remove acts on the stored list, not the page. */
function ExclusionTable({
  exclusions,
  onChange,
}: {
  exclusions: WafExclusion[]
  onChange: (next: WafExclusion[]) => void
}) {
  const [search, setSearch] = useState("")
  const [kindFilter, setKindFilter] = useState("all")
  const matching = exclusions
    .map((e, index) => ({ e, index }))
    .filter(
      ({ e }) =>
        (kindFilter === "all" || e.kind === kindFilter) &&
        matchesQuery(
          [EXCLUSION_KIND_LABEL[e.kind], e.rule_id, e.path, e.arg, e.note],
          search,
        ),
    )
  const pager = usePagedRows(matching, `${search}|${kindFilter}`)
  return (
    <>
      <TableToolbar
        search={search}
        onSearchChange={setSearch}
        searchLabel="Search exclusions"
        placeholder="Search by rule, path or note"
      >
        <TableFilter
          label="Exclusion type"
          value={kindFilter}
          onChange={setKindFilter}
          options={(Object.keys(EXCLUSION_KIND_LABEL) as WafExclusionKind[]).map((k) => ({
            value: k,
            label: EXCLUSION_KIND_LABEL[k],
          }))}
        />
      </TableToolbar>
      {matching.length === 0 ? (
        <p className="text-muted-foreground text-sm">No exclusion matches the filters.</p>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full text-sm">
            <caption className="sr-only">Rule exclusions</caption>
            <thead className="bg-muted/50 text-muted-foreground text-left text-xs">
              <tr>
                <th scope="col" className="px-3 py-1.5 font-medium">Exclusion</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Rule</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Where</th>
                <th scope="col" className="px-3 py-1.5 font-medium">Note</th>
                <th scope="col" className="px-3 py-1.5 font-medium">
                  <span className="sr-only">Remove</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-border divide-y">
              {pager.rows.map(({ e, index: i }) => (
                <tr key={`${e.kind}:${e.rule_id}:${e.path}:${e.arg}:${i}`}>
                  <td className="px-3 py-1.5">{EXCLUSION_KIND_LABEL[e.kind]}</td>
                  <td className="px-3 py-1.5 font-mono text-xs">{e.rule_id}</td>
                  <td className="px-3 py-1.5 font-mono text-xs break-all">
                    {e.kind === "rule" && <span className="text-muted-foreground font-sans">whole site</span>}
                    {e.kind !== "rule" && e.path}
                    {e.kind === "rule_arg" && <span className="block">arg {e.arg}</span>}
                  </td>
                  <td className="text-muted-foreground px-3 py-1.5">{e.note || "—"}</td>
                  <td className="px-3 py-1.5 text-right">
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      aria-label={`Remove exclusion of rule ${e.rule_id}${e.path ? ` on ${e.path}` : ""}`}
                      onClick={() => onChange(exclusions.filter((_, j) => j !== i))}
                    >
                      Remove
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <TablePagination pager={pager} />
    </>
  )
}

function ExclusionEditor({
  exclusions,
  disabled,
  prefill,
  onChange,
}: {
  exclusions: WafExclusion[]
  disabled: boolean
  prefill?: WafPrefill
  onChange: (next: WafExclusion[]) => void
}) {
  const id = useId()
  const prefilled = prefill?.rule !== undefined
  const [kind, setKind] = useState<WafExclusionKind>(
    prefilled && prefill.path ? "rule_path" : "rule",
  )
  const [rule, setRule] = useState(prefilled ? String(prefill.rule) : "")
  const [path, setPath] = useState(prefill?.path ?? "")
  const [arg, setArg] = useState("")
  const [note, setNote] = useState("")
  const [problem, setProblem] = useState<string | null>(null)

  const add = () => {
    const entry: WafExclusion = {
      kind,
      rule_id: rule.trim() === "" ? NaN : Number(rule),
      path: kind === "rule" ? "" : path.trim(),
      arg: kind === "rule_arg" ? arg.trim() : "",
      note: note.trim(),
    }
    const bad = exclusionProblem(entry)
    if (bad) {
      setProblem(bad)
      return
    }
    if (exclusions.length >= WAF_LIMITS.exclusions) {
      setProblem(`At most ${WAF_LIMITS.exclusions} exclusions.`)
      return
    }
    setProblem(null)
    onChange([...exclusions, entry])
    setRule("")
    setPath("")
    setArg("")
    setNote("")
  }

  return (
    <section aria-labelledby={`${id}-heading`} className="flex flex-col gap-3">
      <div className="flex flex-col gap-0.5">
        <h3 id={`${id}-heading`} className="text-sm font-semibold">
          Rule exclusions
        </h3>
        <p className="text-muted-foreground text-xs">
          Stop one ModSecurity rule from matching where it misjudges your application. Add one from a
          blocked request in WAF → WAF audit.
        </p>
      </div>

      {prefilled && (
        <Callout tone="info" role="status" className="text-sm">
          Rule {prefill.rule}
          {prefill.path ? ` on ${prefill.path}` : ""} is filled in below from the audit log. Check it,
          press Add exclusion, then Save and apply.
        </Callout>
      )}

      {exclusions.length === 0 ? (
        <p className="text-muted-foreground text-sm">No exclusions.</p>
      ) : (
        <ExclusionTable exclusions={exclusions} onChange={onChange} />
      )}

      <fieldset
        disabled={disabled}
        className="grid gap-3 rounded-lg border p-3 sm:grid-cols-2"
        aria-label="Add a rule exclusion"
      >
        <div className="flex flex-col gap-1.5 sm:col-span-2">
          <Label htmlFor={`${id}-kind`}>Exclusion type</Label>
          <Select id={`${id}-kind`} value={kind} onChange={(e) => setKind(e.target.value as WafExclusionKind)}>
            {(Object.keys(EXCLUSION_KIND_LABEL) as WafExclusionKind[]).map((k) => (
              <option key={k} value={k}>
                {EXCLUSION_KIND_LABEL[k]}
              </option>
            ))}
          </Select>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-rule`}>Rule id</Label>
          <Input
            id={`${id}-rule`}
            type="number"
            min={1}
            max={WAF_LIMITS.maxRuleId}
            inputMode="numeric"
            placeholder="942100"
            value={rule}
            onChange={(e) => setRule(e.target.value)}
          />
        </div>
        {kind !== "rule" && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-path`}>Path prefix</Label>
            <Input
              id={`${id}-path`}
              value={path}
              placeholder="/admin/save"
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
              aria-invalid={path !== "" && !isValidPath(path)}
              onChange={(e) => setPath(e.target.value)}
            />
          </div>
        )}
        {kind === "rule_arg" && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-arg`}>Argument name</Label>
            <Input
              id={`${id}-arg`}
              value={arg}
              placeholder="comment"
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
              aria-invalid={arg !== "" && !isValidArg(arg)}
              onChange={(e) => setArg(e.target.value)}
            />
          </div>
        )}
        <div className="flex flex-col gap-1.5 sm:col-span-2">
          <Label htmlFor={`${id}-note`}>Note (optional)</Label>
          <Input
            id={`${id}-note`}
            value={note}
            maxLength={WAF_LIMITS.note}
            placeholder="Why this is safe"
            autoComplete="off"
            onChange={(e) => setNote(e.target.value)}
          />
        </div>
        {problem && (
          <p role="alert" className="text-destructive text-xs sm:col-span-2">
            {problem}
          </p>
        )}
        <div className="sm:col-span-2">
          <Button type="button" variant="outline" onClick={add}>
            Add exclusion
          </Button>
        </div>
      </fieldset>
    </section>
  )
}
