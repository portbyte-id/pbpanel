import { useSyncExternalStore } from "react"

/**
 * A tiny toast store. Success acknowledgements go here so every save, delete
 * and service command confirms the same way; a failure that blocks the
 * operator stays inline next to the control, where it can be read and acted on.
 */
export interface Toast {
  id: number
  kind: "success" | "error"
  message: string
}

let toasts: Toast[] = []
let nextId = 1
const listeners = new Set<() => void>()

function emit() {
  for (const l of listeners) l()
}

export function dismissToast(id: number) {
  toasts = toasts.filter((t) => t.id !== id)
  emit()
}

function push(kind: Toast["kind"], message: string) {
  const id = nextId++
  // Keep the stack short: a burst of saves should not bury the page.
  toasts = [...toasts, { id, kind, message }].slice(-4)
  emit()
  // Errors stay longer; they are rarely read in two seconds.
  window.setTimeout(() => dismissToast(id), kind === "error" ? 8000 : 4000)
}

export const toast = {
  success: (message: string) => push("success", message),
  error: (message: string) => push("error", message),
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

export function useToasts(): Toast[] {
  return useSyncExternalStore(subscribe, () => toasts, () => toasts)
}
