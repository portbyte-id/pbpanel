import { Link } from "@tanstack/react-router"
import { X } from "lucide-react"
import { useId, useState } from "react"

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
import { ApiError } from "@/lib/api"
import {
  configProblems,
  EXCLUSION_KIND_LABEL,
  exclusionProblem,
  isValidArg,
  isValidIPEntry,
  isValidPath,
  MODE_LABEL,
  PARANOIA_HELP,
  splitList,
  WAF_LIMITS,
  type WafConfig,
  type WafExclusion,
  type WafExclusionKind,
  type WafMode,
  type WafSiteStatus,
} from "@/lib/waf"
import { useSaveSiteWaf, useSiteWaf } from "@/lib/wafQueries"

/** A rule to exclude, handed over from the audit viewer through the URL. */
export interface WafPrefill {
  rule?: number
  path?: string
}

/**
 * The firewall of one site: ModSecurity with the OWASP Core Rule Set, and the
 * native nginx protections. One Save writes the whole configuration, and nginx
 * checks it (`nginx -t`) before anything is applied.
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
  // Keyed on what the server holds, so a save or a reload resets the form to it.
  return (
    <WafForm
      key={JSON.stringify(data.config)}
      domain={domain}
      saved={data.config}
      status={data.status}
      prefill={prefill}
    />
  )
}

function WafForm({
  domain,
  saved,
  status,
  prefill,
}: {
  domain: string
  saved: WafConfig
  status: WafSiteStatus
  prefill?: WafPrefill
}) {
  const id = useId()
  const save = useSaveSiteWaf(domain)
  const [draft, setDraft] = useState<WafConfig>(saved)
  const [confirming, setConfirming] = useState(false)

  const dirty = JSON.stringify(draft) !== JSON.stringify(saved)
  const problems = configProblems(draft)
  const problemList = Object.values(problems)
  const set = (patch: Partial<WafConfig>) => {
    save.reset()
    setDraft((d) => ({ ...d, ...patch }))
  }

  const submit = () => {
    save.mutate(draft, { onSuccess: () => setConfirming(false) })
  }
  const onSave = () => {
    if (draft.mode === "block" && saved.mode !== "block") setConfirming(true)
    else submit()
  }

  const modsecOff = !status.available
  const rl = draft.rate_limit

  return (
    <form
      className="flex flex-col gap-8"
      onSubmit={(e) => {
        e.preventDefault()
        if (dirty && problemList.length === 0) onSave()
      }}
      aria-label={`Firewall for ${domain}`}
    >
      {/* --- ModSecurity ---------------------------------------------------- */}
      <section aria-labelledby={`${id}-modsec`} className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <h3 id={`${id}-modsec`} className="text-sm font-semibold">
            ModSecurity
          </h3>
          {status.available && <Badge variant="secondary">OWASP CRS{status.crs_version ? ` ${status.crs_version}` : ""}</Badge>}
        </div>

        {modsecOff && (
          <Callout tone="warning" role="status" className="text-sm">
            <p className="font-medium">ModSecurity is not available on this server.</p>
            <p className="text-muted-foreground mt-1">
              {status.reason ||
                "It is built into the post-quantum nginx flavor, together with the OWASP Core Rule Set."}{" "}
              Switch the web server to that flavor on the{" "}
              <Link to="/nginx" className="underline underline-offset-4">
                Nginx page
              </Link>{" "}
              to turn it on. The native protections below work with any flavor.
            </p>
          </Callout>
        )}

        <fieldset className="flex flex-col gap-1.5">
          <legend className="text-sm font-medium">Mode</legend>
          <div role="radiogroup" aria-label="ModSecurity mode" className="inline-flex flex-wrap gap-1">
            {(["off", "detect", "block"] as WafMode[]).map((m) => {
              const unavailable = modsecOff && m !== "off"
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
            {draft.mode === "off" && "Requests are not inspected by ModSecurity."}
            {draft.mode === "detect" &&
              "Requests are inspected and matches are logged to the audit log, but nothing is blocked. Use this first to find false positives."}
            {draft.mode === "block" &&
              "Requests that score over the threshold are refused with 403, and logged."}
          </p>
        </fieldset>

        <div className="flex max-w-md flex-col gap-1.5">
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

        {!status.attached && (
          <p className="text-muted-foreground text-xs">
            The site file has no firewall block yet. Saving adds one; the previous version of the file
            stays in its history.
          </p>
        )}
      </section>

      {/* --- Native protections --------------------------------------------- */}
      <section aria-labelledby={`${id}-native`} className="flex flex-col gap-5">
        <div className="flex flex-col gap-0.5">
          <h3 id={`${id}-native`} className="text-sm font-semibold">
            Native nginx protections
          </h3>
          <p className="text-muted-foreground text-xs">
            Evaluated by nginx itself, before the request reaches your site. They work without ModSecurity.
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

      {/* --- Exclusions ----------------------------------------------------- */}
      <ExclusionEditor
        exclusions={draft.exclusions}
        disabled={modsecOff && draft.exclusions.length === 0}
        prefill={prefill}
        onChange={(exclusions) => set({ exclusions })}
      />

      {/* --- Save ------------------------------------------------------------ */}
      <div className="bg-background/95 sticky bottom-0 -mx-4 flex flex-col gap-3 border-t px-4 py-3 backdrop-blur-sm">
        <ApplyError error={save.error} fallback="The firewall settings could not be saved." />
        {problemList.length > 0 && (
          <p role="alert" className="text-destructive text-xs">
            Fix the highlighted values to save.
          </p>
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
          Blocking mode affects live traffic: a request that ModSecurity scores as an attack gets a 403
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
          blocked request in Logs → WAF audit.
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
              {exclusions.map((e, i) => (
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
