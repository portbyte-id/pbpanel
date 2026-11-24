/**
 * The Markdown subset release notes use: headings, bullet lists and paragraphs.
 * Anything else stays as plain text, which is the safe default for text that
 * comes from GitHub. Nothing here produces HTML; the component renders the
 * parsed blocks as React elements.
 */
export type NoteBlock =
  | { kind: "heading"; level: 1 | 2 | 3; text: string }
  | { kind: "list"; items: string[] }
  | { kind: "paragraph"; text: string }

export function parseReleaseNotes(markdown: string): NoteBlock[] {
  const blocks: NoteBlock[] = []
  let list: string[] | null = null
  let paragraph: string[] = []

  const flushParagraph = () => {
    if (paragraph.length > 0) {
      blocks.push({ kind: "paragraph", text: paragraph.join(" ") })
      paragraph = []
    }
  }
  const flushList = () => {
    if (list !== null) {
      blocks.push({ kind: "list", items: list })
      list = null
    }
  }

  for (const raw of markdown.split(/\r?\n/)) {
    const line = raw.trimEnd()
    const heading = /^(#{1,3})\s+(.*)$/.exec(line)
    const bullet = /^\s*[-*+]\s+(.*)$/.exec(line)

    if (heading) {
      flushParagraph()
      flushList()
      blocks.push({ kind: "heading", level: heading[1].length as 1 | 2 | 3, text: heading[2].trim() })
    } else if (bullet) {
      flushParagraph()
      if (list === null) {
        list = []
      }
      list.push(bullet[1].trim())
    } else if (line.trim() === "") {
      flushParagraph()
      flushList()
    } else {
      flushList()
      paragraph.push(line.trim())
    }
  }
  flushParagraph()
  flushList()
  return blocks
}
