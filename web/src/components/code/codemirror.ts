import { defaultKeymap, historyKeymap, indentWithTab } from "@codemirror/commands"
import { HighlightStyle, StreamLanguage, syntaxHighlighting } from "@codemirror/language"
import { css } from "@codemirror/legacy-modes/mode/css"
import { go } from "@codemirror/legacy-modes/mode/go"
import { javascript, json, typescript } from "@codemirror/legacy-modes/mode/javascript"
import { nginx } from "@codemirror/legacy-modes/mode/nginx"
import { properties } from "@codemirror/legacy-modes/mode/properties"
import { python } from "@codemirror/legacy-modes/mode/python"
import { shell } from "@codemirror/legacy-modes/mode/shell"
import { standardSQL } from "@codemirror/legacy-modes/mode/sql"
import { html, xml } from "@codemirror/legacy-modes/mode/xml"
import { yaml } from "@codemirror/legacy-modes/mode/yaml"
import { searchKeymap } from "@codemirror/search"
import type { Extension } from "@codemirror/state"
import { EditorView, keymap } from "@codemirror/view"
import { tags as t } from "@lezer/highlight"

/**
 * Syntax colours read from the `--code-*` tokens in index.css. The default
 * CodeMirror palette is dark text for a light page, which is unreadable on the
 * dark surface; these follow the page's light or dark palette instead.
 */
export const codeHighlight = syntaxHighlighting(
  HighlightStyle.define([
    { tag: [t.keyword, t.operatorKeyword, t.modifier], color: "var(--code-keyword)" },
    { tag: [t.string, t.regexp, t.special(t.string)], color: "var(--code-string)" },
    { tag: [t.number, t.bool, t.null, t.atom], color: "var(--code-number)" },
    {
      tag: [t.comment, t.lineComment, t.blockComment, t.docComment],
      color: "var(--code-comment)",
      fontStyle: "italic",
    },
    { tag: [t.tagName, t.labelName], color: "var(--code-name)" },
    { tag: [t.attributeName, t.propertyName], color: "var(--code-prop)" },
    { tag: [t.typeName, t.className, t.namespace], color: "var(--code-type)" },
    { tag: [t.meta, t.processingInstruction, t.macroName], color: "var(--code-meta)" },
    { tag: t.invalid, color: "var(--code-invalid)" },
    { tag: t.heading, fontWeight: "600" },
    { tag: t.link, textDecoration: "underline" },
    { tag: t.strong, fontWeight: "600" },
    { tag: t.emphasis, fontStyle: "italic" },
  ]),
  { fallback: true },
)

/**
 * The editor's chrome, taken from the app's tokens. The font size is a variable
 * so a page can change it without a new theme.
 */
export const codeTheme = EditorView.theme({
  "&": {
    color: "var(--foreground)",
    backgroundColor: "var(--background)",
    fontSize: "var(--editor-font-size, 13px)",
  },
  "&.cm-focused": { outline: "none" },
  ".cm-content": { caretColor: "var(--foreground)" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--foreground)" },
  ".cm-selectionBackground, &.cm-focused .cm-selectionBackground": {
    background: "color-mix(in oklch, var(--primary) 30%, transparent) !important",
  },
  ".cm-scroller": {
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
    lineHeight: "1.55",
  },
  ".cm-gutters": {
    backgroundColor: "var(--muted)",
    color: "var(--muted-foreground)",
    border: "none",
  },
  ".cm-activeLine": { backgroundColor: "color-mix(in oklch, var(--muted) 60%, transparent)" },
  ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--foreground)" },
  ".cm-matchingBracket": {
    backgroundColor: "color-mix(in oklch, var(--primary) 22%, transparent)",
    color: "inherit !important",
  },
  // The search and go-to-line panels: they inherit the editor's colours.
  ".cm-panels": {
    backgroundColor: "var(--card)",
    color: "var(--foreground)",
    borderColor: "var(--border)",
  },
  ".cm-panel input, .cm-panel button": {
    fontFamily: "var(--font-sans)",
    fontSize: "13px",
    borderRadius: "var(--radius-sm)",
  },
  ".cm-panel input": {
    backgroundColor: "var(--background)",
    color: "var(--foreground)",
    border: "1px solid var(--input)",
  },
  ".cm-panel button": {
    backgroundColor: "var(--secondary)",
    color: "var(--secondary-foreground)",
    border: "1px solid var(--border)",
  },
  ".cm-searchMatch": {
    backgroundColor: "color-mix(in oklch, var(--warning) 35%, transparent)",
  },
  ".cm-searchMatch-selected": {
    backgroundColor: "color-mix(in oklch, var(--primary) 45%, transparent)",
  },
})

/** The language modes this editor knows, keyed by the server's language name. */
const modes = {
  css: StreamLanguage.define(css),
  go: StreamLanguage.define(go),
  html: StreamLanguage.define(html),
  javascript: StreamLanguage.define(javascript),
  json: StreamLanguage.define(json),
  nginx: StreamLanguage.define(nginx),
  properties: StreamLanguage.define(properties),
  python: StreamLanguage.define(python),
  shell: StreamLanguage.define(shell),
  sql: StreamLanguage.define(standardSQL),
  typescript: StreamLanguage.define(typescript),
  xml: StreamLanguage.define(xml),
  yaml: StreamLanguage.define(yaml),
} satisfies Record<string, Extension>

type ModeName = keyof typeof modes

/** A PHP file is mostly markup with PHP islands; the HTML mode reads it better than none. */
const aliases: Record<string, ModeName> = { php: "html", ini: "properties" }

/** The highlighting for a language name from the server; plain text when there is none. */
export function languageExtension(name: string): Extension {
  const key = aliases[name] ?? name
  return key in modes ? modes[key as ModeName] : []
}

const labels: Record<string, string> = {
  css: "CSS",
  go: "Go",
  html: "HTML",
  javascript: "JavaScript",
  json: "JSON",
  markdown: "Markdown",
  nginx: "Nginx",
  php: "PHP",
  properties: "Properties",
  python: "Python",
  shell: "Shell",
  sql: "SQL",
  typescript: "TypeScript",
  xml: "XML",
  yaml: "YAML",
}

/** The name a person reads in the status bar. */
export function languageLabel(name: string): string {
  return labels[name] ?? "Plain text"
}

/** Keys every editor here shares: the defaults, undo and redo, Tab to indent, and find. */
export const editorKeymap = keymap.of([
  ...defaultKeymap,
  ...historyKeymap,
  indentWithTab,
  ...searchKeymap,
])
