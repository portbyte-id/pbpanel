import { useId, useState, type FormEvent } from "react"

import { ErrorState } from "@/components/ErrorState"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { SkeletonRows } from "@/components/ui/skeleton"
import { Textarea } from "@/components/ui/textarea"
import { GLOBAL_LIMITS, type WafGlobal, type WafGlobalResponse } from "@/lib/waf"
import { useSaveWafGlobal, useWafGlobal } from "@/lib/wafQueries"

/** The directive nginx replaces with the request's id, so support can find the request in the log. */
const REQUEST_ID_TAG = '<!--#echo var="request_id" -->'
const SAMPLE_ID = "PB-7F3A92C1"

/** The page a visitor sees when the firewall refuses a request. */
export function BlockPagePanel() {
  const global = useWafGlobal()
  if (global.isPending) return <SkeletonRows label="Loading the block page…" />
  if (global.error) {
    return (
      <ErrorState
        error={global.error}
        fallback="The block page is not available."
        onRetry={() => void global.refetch()}
      />
    )
  }
  return <BlockPageForm key={JSON.stringify(global.data.global.error_page)} data={global.data} />
}

function BlockPageForm({ data }: { data: WafGlobalResponse }) {
  const id = useId()
  const save = useSaveWafGlobal()
  const current = data.global
  const [enabled, setEnabled] = useState(current.error_page.enabled)
  const [html, setHtml] = useState(current.error_page.html)

  const bytes = new TextEncoder().encode(html).length
  const tooBig = bytes > GLOBAL_LIMITS.pageBytes
  const hasTag = html.includes(REQUEST_ID_TAG)
  // The preview runs no script and shows the request id a real block would carry.
  const preview = html.split(REQUEST_ID_TAG).join(SAMPLE_ID)

  function submit(e: FormEvent) {
    e.preventDefault()
    if (tooBig) return
    const next: WafGlobal = { ...current, error_page: { enabled, html } }
    save.mutate(next)
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" aria-label="Block page">
      <label className="flex items-center gap-2 text-sm font-medium">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        Show this page for refused requests
      </label>
      <p className="text-muted-foreground text-xs">
        Used for requests the firewall refuses on every site and on sites with their own firewall. The status code
        stays as it is: 403 for a refusal, 429 for a rate limit. When this is off, visitors see nginx&apos;s plain
        error page.
      </p>

      <div className="grid gap-4 xl:grid-cols-2">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-html`}>HTML</Label>
          <Textarea
            id={`${id}-html`}
            rows={22}
            spellCheck={false}
            className="font-mono text-xs"
            value={html}
            onChange={(e) => setHtml(e.target.value)}
            aria-invalid={tooBig}
            aria-describedby={`${id}-hint`}
          />
          <p id={`${id}-hint`} className={tooBig ? "text-destructive text-xs" : "text-muted-foreground text-xs"}>
            {tooBig
              ? `The page is larger than ${GLOBAL_LIMITS.pageBytes / 1024} KB.`
              : `Include ${REQUEST_ID_TAG} to show the reference number. Scripts and external stylesheets are not loaded from this page; keep styles inline. ${bytes.toLocaleString()} bytes.`}
          </p>
          {!hasTag && (
            <p className="text-muted-foreground text-xs">
              The page has no reference number; visitors cannot quote one to the site owner.
            </p>
          )}
        </div>

        <div className="flex flex-col gap-1.5">
          <span className="text-sm font-medium">Preview</span>
          <iframe
            title="Block page preview"
            sandbox=""
            srcDoc={preview}
            className="bg-background h-[28rem] w-full rounded-md border"
          />
          <p className="text-muted-foreground text-xs">The reference is a sample; a real block shows the request&apos;s own id.</p>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit" disabled={save.isPending || tooBig}>
          {save.isPending ? "Applying…" : "Save block page"}
        </Button>
        <Button type="button" variant="outline" onClick={() => setHtml(data.default_html)}>
          Reset to the default page
        </Button>
      </div>

      {save.error && (
        <p role="alert" className="text-destructive text-sm">
          {save.error.message}
        </p>
      )}
    </form>
  )
}
