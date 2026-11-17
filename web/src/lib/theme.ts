import { useCallback, useEffect, useState } from "react"

export type ThemeMode = "system" | "light" | "dark"

const KEY = "panel.theme"

function read(): ThemeMode {
  try {
    const v = window.localStorage.getItem(KEY)
    if (v === "light" || v === "dark" || v === "system") return v
  } catch {
    // Storage can be blocked; the system preference is the fallback.
  }
  return "system"
}

function prefersDark(): boolean {
  return typeof window.matchMedia === "function" && window.matchMedia("(prefers-color-scheme: dark)").matches
}

/** Puts the `dark` class on <html> for the mode. Safe to call before React mounts. */
export function applyTheme(mode: ThemeMode = read()) {
  const dark = mode === "dark" || (mode === "system" && prefersDark())
  document.documentElement.classList.toggle("dark", dark)
}

/** The chosen mode and a setter that persists it. "system" follows the OS live. */
export function useTheme(): [ThemeMode, (mode: ThemeMode) => void] {
  const [mode, setModeState] = useState<ThemeMode>(read)

  useEffect(() => {
    applyTheme(mode)
    if (mode !== "system" || typeof window.matchMedia !== "function") return
    const query = window.matchMedia("(prefers-color-scheme: dark)")
    const onChange = () => applyTheme("system")
    query.addEventListener("change", onChange)
    return () => query.removeEventListener("change", onChange)
  }, [mode])

  const setMode = useCallback((next: ThemeMode) => {
    try {
      window.localStorage.setItem(KEY, next)
    } catch {
      // Not persisted; still applies for this page.
    }
    setModeState(next)
  }, [])

  return [mode, setMode]
}
