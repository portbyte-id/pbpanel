import type { LucideIcon } from "lucide-react"
import {
  AppWindow,
  Archive,
  FileText,
  FolderTree,
  Gauge,
  Globe,
  LockKeyhole,
  Network,
  Settings,
  ShieldCheck,
  Terminal,
} from "lucide-react"

/**
 * The sidebar, in milestone order. `path` is set only for a section that exists
 * in this build: a section without one is rendered disabled with its milestone,
 * because a link to a route that was never written is a dead end the operator
 * has to discover by clicking it.
 */
export interface NavSection {
  label: string
  icon: LucideIcon
  path?: string
  /** Shown as the disabled hint: the milestone that builds this section. */
  milestone?: string
}

/** Sections for future milestones. Every one of these is disabled today. */
export const futureSections: NavSection[] = [
  { label: "Dashboard", icon: Gauge, milestone: "M5" },

  { label: "Files", icon: FolderTree, milestone: "M4" },
  { label: "Firewall", icon: ShieldCheck, milestone: "M4" },
  { label: "Apps", icon: AppWindow, milestone: "M5" },
  { label: "TLS", icon: LockKeyhole, milestone: "M3" },
  { label: "Docker", icon: Archive, milestone: "M8" },
]

/** Sections this build implements. */
export const builtSections: NavSection[] = [
  { label: "Sites", icon: Globe, path: "/sites" },
  { label: "Nginx", icon: Network, path: "/nginx" },
  { label: "PHP", icon: Terminal, path: "/php" },
  { label: "Settings", icon: Settings, path: "/settings" },
  { label: "Audit", icon: FileText, path: "/audit" },
]
