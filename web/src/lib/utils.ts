import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"
import type { KeyboardEvent } from "react"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

/**
 * Whether a field may grab focus on open. A touch device's soft keyboard
 * covers half the screen, so only fine pointers (mouse, trackpad) auto-focus.
 */
export function canAutoFocus(): boolean {
  return typeof window.matchMedia !== "function" || window.matchMedia("(pointer: fine)").matches
}

/** Arrow/Home/End navigation for a `role="tablist"`: moves to the neighbouring tab and activates it. */
export function tablistKeyDown(e: KeyboardEvent<HTMLElement>) {
  const keys = ["ArrowLeft", "ArrowRight", "Home", "End"]
  if (!keys.includes(e.key)) return
  const tabs = Array.from(e.currentTarget.querySelectorAll<HTMLElement>('[role="tab"]:not([disabled])'))
  const i = tabs.indexOf(document.activeElement as HTMLElement)
  if (i < 0 || tabs.length === 0) return
  e.preventDefault()
  const next =
    e.key === "Home" ? 0 : e.key === "End" ? tabs.length - 1 : (i + (e.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length
  tabs[next].focus()
  tabs[next].click()
}
