import type { LucideIcon } from "lucide-react"
import {
  AppWindow,
  Archive,
  Cpu,
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
  { label: "Dashboard", icon: Gauge, milestone: "M7" },
  { label: "TLS", icon: LockKeyhole, milestone: "M6" },
  { label: "Docker", icon: Archive, milestone: "M8" },
]

/** Sections this build implements. */
export const builtSections: NavSection[] = [
  { label: "Sites", icon: Globe, path: "/sites" },
  { label: "Files", icon: FolderTree, path: "/files" },
  { label: "Nginx", icon: Network, path: "/nginx" },
  { label: "PHP", icon: Terminal, path: "/php" },
  { label: "Firewall", icon: ShieldCheck, path: "/firewall" },
  { label: "Apps", icon: AppWindow, path: "/apps" },
  { label: "Runtimes", icon: Cpu, path: "/runtimes" },
  { label: "Settings", icon: Settings, path: "/settings" },
  { label: "Audit", icon: FileText, path: "/audit" },
]
