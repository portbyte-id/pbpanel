import { history, undoDepth } from "@codemirror/commands"
import { bracketMatching, indentUnit } from "@codemirror/language"
import { search } from "@codemirror/search"
import { Compartment, EditorState } from "@codemirror/state"
import { EditorView, highlightActiveLine, keymap, lineNumbers } from "@codemirror/view"
import { type CSSProperties, useEffect, useRef } from "react"

import {
  codeHighlight,
  codeTheme,
  editorKeymap,
  languageExtension,
} from "@/components/code/codemirror"

/** Where the caret is and how much undo history the document holds. */
export interface EditorStatus {
  line: number
  col: number
  history: number
}

export type EditorPalette = "auto" | "light" | "dark"

/** The status line's numbers for a state: the caret's line and column, and the undo depth. */
export function statusOf(state: EditorState): EditorStatus {
  const head = state.selection.main.head
  const line = state.doc.lineAt(head)
  return { line: line.number, col: head - line.from + 1, history: undoDepth(state) }
}

/**
 * One document in a CodeMirror view, kept alive while its tab is open so the
 * undo history and caret survive switching tabs. `hidden` keeps the view
 * mounted but out of the layout.
 *
 * The text is LF-only: the caller converts CRLF on the way in and out, so the
 * view and the draft always compare equal.
 */
export function TextEditor({
  label,
  value,
  language,
  fontSize,
  tabSize,
  wrap,
  palette = "auto",
  readOnly = false,
  hidden = false,
  onChange,
  onStatus,
  onSave,
  onView,
}: {
  label: string
  value: string
  language: string
  fontSize: number
  tabSize: number
  wrap: boolean
  palette?: EditorPalette
  readOnly?: boolean
  hidden?: boolean
  onChange?: (value: string) => void
  onStatus?: (status: EditorStatus) => void
  onSave?: () => void
  /** Receives the live view on mount and null on unmount; the toolbar drives search and go-to-line through it. */
  onView?: (view: EditorView | null) => void
}) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const tabs = useRef(new Compartment())
  const wrapping = useRef(new Compartment())
  const editable = useRef(new Compartment())

  // The view is built once; these refs let its listeners reach the latest props.
  const changed = useRef(onChange)
  const status = useRef(onStatus)
  const save = useRef(onSave)
  const viewed = useRef(onView)
  changed.current = onChange
  status.current = onStatus
  save.current = onSave
  viewed.current = onView

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
          search({ top: true }),
          languageExtension(language),
          codeHighlight,
          codeTheme,
          editorKeymap,
          keymap.of([
            {
              key: "Mod-s",
              preventDefault: true,
              run: () => {
                save.current?.()
                return true
              },
            },
          ]),
          EditorView.contentAttributes.of({ "aria-label": label }),
          tabs.current.of([EditorState.tabSize.of(tabSize), indentUnit.of(" ".repeat(tabSize))]),
          wrapping.current.of(wrap ? EditorView.lineWrapping : []),
          editable.current.of([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]),
          EditorView.updateListener.of((update) => {
            if (update.docChanged) changed.current?.(update.state.doc.toString())
            if (update.docChanged || update.selectionSet) {
              status.current?.(statusOf(update.state))
            }
          }),
        ],
      }),
    })
    view.current = created
    status.current?.(statusOf(created.state))
    viewed.current?.(created)
    return () => {
      viewed.current?.(null)
      created.destroy()
      view.current = null
    }
    // The view is built once per mount; the effects below apply later changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    const v = view.current
    if (!v || v.state.doc.toString() === value) return
    v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } })
  }, [value])

  useEffect(() => {
    view.current?.dispatch({
      effects: tabs.current.reconfigure([
        EditorState.tabSize.of(tabSize),
        indentUnit.of(" ".repeat(tabSize)),
      ]),
    })
  }, [tabSize])

  useEffect(() => {
    view.current?.dispatch({
      effects: wrapping.current.reconfigure(wrap ? EditorView.lineWrapping : []),
    })
  }, [wrap])

  useEffect(() => {
    view.current?.dispatch({
      effects: editable.current.reconfigure([
        EditorState.readOnly.of(readOnly),
        EditorView.editable.of(!readOnly),
      ]),
    })
  }, [readOnly])

  // A view laid out while hidden has no size; measure again once it shows.
  useEffect(() => {
    if (!hidden) view.current?.requestMeasure()
  }, [hidden])

  const palettes: Record<EditorPalette, string> = { auto: "", light: "code-light", dark: "code-dark" }
  const sizeVar = { "--editor-font-size": `${fontSize}px` } as CSSProperties

  return (
    <div
      ref={host}
      hidden={hidden}
      style={sizeVar}
      className={`${palettes[palette]} border-input flex min-h-0 flex-1 flex-col overflow-hidden [&>.cm-editor]:min-h-0 [&>.cm-editor]:flex-1`}
    />
  )
}
