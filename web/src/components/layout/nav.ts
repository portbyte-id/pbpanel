import type { LucideIcon } from "lucide-react"
import {
  Activity,
  AppWindow,
  CalendarClock,
  Cpu,
  FileText,
  FolderTree,
  Gauge,
  Globe,
  LockKeyhole,
  Network,
  ScrollText,
  Server,
  Settings,
  ShieldCheck,
  Terminal,
} from "lucide-react"

/**
 * The navigation model. Pages are grouped by what the operator is doing, not by
 * the milestone that built them. `description` is the one-line subtitle the
 * header shows under the page title and the command palette shows beside the name.
 */
export interface NavItem {
  label: string
  icon: LucideIcon
  path: string
  description: string
  /** Extra words the command palette matches on. */
  keywords?: string
}

export interface NavGroup {
  label: string
  items: NavItem[]
}

/** Sections for future milestones. Rendered disabled, with the milestone as the hint. */
export interface FutureSection {
  label: string
  icon: LucideIcon
  milestone: string
}

export const futureSections: FutureSection[] = []

export const navGroups: NavGroup[] = [
  {
    label: "Overview",
    items: [
      {
        label: "Dashboard",
        icon: Gauge,
        path: "/dashboard",
        description: "Health, resources and what needs attention",
        keywords: "home status cpu memory",
      },
      {
        label: "Services",
        icon: Server,
        path: "/services",
        description: "System services, containers and apps, with start, stop and restart",
        keywords: "systemd containers restart",
      },
    ],
  },
  {
    label: "Web",
    items: [
      { label: "Sites", icon: Globe, path: "/sites", description: "Domains served by nginx", keywords: "domains vhost" },
      { label: "Files", icon: FolderTree, path: "/files", description: "Browse, edit and upload files", keywords: "file manager upload" },
      { label: "Nginx", icon: Network, path: "/nginx", description: "Web server settings and configuration", keywords: "config reload" },
      { label: "PHP", icon: Terminal, path: "/php", description: "PHP versions, pools and ini settings", keywords: "fpm" },
      { label: "TLS", icon: LockKeyhole, path: "/tls", description: "Certificates and renewals", keywords: "ssl https acme letsencrypt" },
    ],
  },
  {
    label: "Server",
    items: [
      { label: "Firewall", icon: ShieldCheck, path: "/firewall", description: "Open ports and rules", keywords: "iptables ports" },
      { label: "Cron", icon: CalendarClock, path: "/cron", description: "Scheduled jobs and their run history", keywords: "schedule" },
      { label: "Traffic", icon: Activity, path: "/traffic", description: "Live requests, errors and firewall events", keywords: "waf requests monitor modsecurity" },
      { label: "Logs", icon: ScrollText, path: "/logs", description: "Live and recent log files, and the WAF audit", keywords: "tail waf audit modsecurity" },
      { label: "Apps", icon: AppWindow, path: "/apps", description: "Node and custom apps run by the panel", keywords: "node process" },
      { label: "Runtimes", icon: Cpu, path: "/runtimes", description: "Installed language runtimes and images", keywords: "docker image php node" },
    ],
  },
  {
    label: "Panel",
    items: [
      { label: "Settings", icon: Settings, path: "/settings", description: "Panel configuration", keywords: "config env" },
      { label: "Audit", icon: FileText, path: "/audit", description: "Who changed what, and when", keywords: "log history" },
    ],
  },
]

/** Every page, flat, in nav order. */
export const builtSections: NavItem[] = navGroups.flatMap((g) => g.items)

/** The nav entry for a pathname (`/sites/example.com` belongs to Sites), or null. */
export function navItemFor(path: string): NavItem | null {
  return builtSections.find((item) => path === item.path || path.startsWith(`${item.path}/`)) ?? null
}
