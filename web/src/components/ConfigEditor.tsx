import { history } from "@codemirror/commands"
import { bracketMatching, StreamLanguage } from "@codemirror/language"
import { nginx } from "@codemirror/legacy-modes/mode/nginx"
import { properties } from "@codemirror/legacy-modes/mode/properties"
import { unifiedMergeView } from "@codemirror/merge"
import { Compartment, EditorState } from "@codemirror/state"
import { EditorView, highlightActiveLine, lineNumbers } from "@codemirror/view"
import { useEffect, useRef } from "react"

import { codeHighlight, codeTheme, editorKeymap } from "@/components/code/codemirror"

/** A config file is read top to bottom, so the box keeps a readable height and scrolls inside. */
const sizing = EditorView.theme({
  ".cm-scroller": { minHeight: "18rem", maxHeight: "60vh" },
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
          codeHighlight,
          editorKeymap,
          EditorView.contentAttributes.of({ "aria-label": label }),
          codeTheme,
          sizing,
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
