import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands"
import {
  bracketMatching,
  defaultHighlightStyle,
  StreamLanguage,
  syntaxHighlighting,
} from "@codemirror/language"
import { nginx } from "@codemirror/legacy-modes/mode/nginx"
import { properties } from "@codemirror/legacy-modes/mode/properties"
import { unifiedMergeView } from "@codemirror/merge"
import { Compartment, EditorState } from "@codemirror/state"
import { EditorView, highlightActiveLine, keymap, lineNumbers } from "@codemirror/view"
import { useEffect, useRef } from "react"

/**
 * The editor's look, taken from the app's tokens so it follows light and dark
 * with the rest of the page instead of bringing a theme of its own.
 */
const theme = EditorView.theme({
  "&": {
    color: "var(--foreground)",
    backgroundColor: "var(--background)",
    fontSize: "13px",
  },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": {
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
    lineHeight: "1.55",
    minHeight: "18rem",
    maxHeight: "60vh",
  },
  ".cm-gutters": {
    backgroundColor: "var(--muted)",
    color: "var(--muted-foreground)",
    border: "none",
  },
  ".cm-activeLine": { backgroundColor: "color-mix(in oklch, var(--muted) 60%, transparent)" },
  ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--foreground)" },
})

/**
 * A CodeMirror editor for a config file: nginx syntax, or ini for php.ini.
 *
 * `value` is the text to show. The editor owns its document while the operator
 * types, and is only overwritten when `value` differs from what it holds: after
 * a discard, a restore, or loading an older version.
 *
 * `original`, when given, turns on a unified diff against that text: removed
 * lines appear above the lines that replaced them.
 */
export function ConfigEditor({
  value,
  onChange,
  original,
  label,
  language = "nginx",
}: {
  value: string
  onChange: (value: string) => void
  original?: string
  label: string
  /** Fixed for the life of the editor. */
  language?: "nginx" | "ini"
}) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const diff = useRef(new Compartment())
  // The listener is created once; the ref keeps it calling the current handler.
  const handler = useRef(onChange)
  handler.current = onChange

  useEffect(() => {
    if (!host.current) return
    const created = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          history(),
          bracketMatching(),
          highlightActiveLine(),
          StreamLanguage.define(language === "ini" ? properties : nginx),
          syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
          keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
          EditorView.contentAttributes.of({ "aria-label": label }),
          theme,
          diff.current.of([]),
          EditorView.updateListener.of((update) => {
            if (update.docChanged) handler.current(update.state.doc.toString())
          }),
        ],
      }),
    })
    view.current = created
    return () => {
      created.destroy()
      view.current = null
    }
    // The view is built once per mount; later changes arrive through the
    // effects below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    const v = view.current
    if (!v || v.state.doc.toString() === value) return
    v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } })
  }, [value])

  useEffect(() => {
    view.current?.dispatch({
      effects: diff.current.reconfigure(
        original === undefined
          ? []
          : unifiedMergeView({ original, mergeControls: false, gutter: true }),
      ),
    })
  }, [original])

  return <div ref={host} className="border-input overflow-hidden rounded-md border" />
}
