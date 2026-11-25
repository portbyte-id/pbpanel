import { Fragment, type ReactNode } from "react"

import { parseReleaseNotes } from "@/lib/release-notes"

/**
 * Release notes as text: headings, bullet lists and paragraphs, with `code` and
 * **bold** inside them. Everything renders as React text, so nothing in the notes
 * can inject markup.
 */
export function ReleaseNotes({ markdown }: { markdown: string }) {
  const blocks = parseReleaseNotes(markdown)
  if (blocks.length === 0) {
    return <p className="text-muted-foreground text-sm">No release notes were published for this version.</p>
  }

  return (
    <div className="space-y-3 text-sm leading-relaxed">
      {blocks.map((block, index) => {
        switch (block.kind) {
          case "heading":
            return (
              <p key={index} className={block.level === 1 ? "text-base font-semibold" : "font-semibold"}>
                <Inline text={block.text} />
              </p>
            )
          case "list":
            return (
              <ul key={index} className="list-disc space-y-1 pl-5">
                {block.items.map((item, i) => (
                  <li key={i}>
                    <Inline text={item} />
                  </li>
                ))}
              </ul>
            )
          case "paragraph":
            return (
              <p key={index}>
                <Inline text={block.text} />
              </p>
            )
        }
      })}
    </div>
  )
}

/** Splits out `code` and **bold** spans; everything else is plain text. */
function Inline({ text }: { text: string }): ReactNode {
  const parts = text.split(/(`[^`]+`|\*\*[^*]+\*\*)/g)
  return parts.map((part, i) => {
    if (part.length > 2 && part.startsWith("`") && part.endsWith("`")) {
      return (
        <code translate="no" key={i} className="bg-muted rounded px-1 py-0.5 font-mono text-[0.85em]">
          {part.slice(1, -1)}
        </code>
      )
    }
    if (part.length > 4 && part.startsWith("**") && part.endsWith("**")) {
      return <strong key={i}>{part.slice(2, -2)}</strong>
    }
    return <Fragment key={i}>{part}</Fragment>
  })
}
