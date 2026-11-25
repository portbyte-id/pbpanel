import type { LucideIcon } from "lucide-react"
import {
  Activity,
  Bell,
  CalendarClock,
  Container,
  Cpu,
  FileText,
  FolderTree,
  Gauge,
  Globe,
  ListChecks,
  LockKeyhole,
  Package,
  ScrollText,
  Server,
  Settings,
  ShieldAlert,
  ShieldCheck,
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
      {
        label: "Jobs",
        icon: ListChecks,
        path: "/jobs",
        description: "Background work running now, and the output of recent jobs",
        keywords: "tasks background progress output watch queue",
      },
    ],
  },
  {
    label: "Web",
    items: [
      { label: "Sites", icon: Globe, path: "/sites", description: "Domains served by nginx", keywords: "domains vhost" },
      { label: "Files", icon: FolderTree, path: "/files", description: "Browse, edit and upload files", keywords: "file manager upload" },
      { label: "TLS", icon: LockKeyhole, path: "/tls", description: "Certificates and renewals", keywords: "ssl https acme letsencrypt" },
      { label: "Runtimes", icon: Cpu, path: "/runtimes", description: "Nginx, PHP and Node.js: versions, builds and settings", keywords: "nginx php node fpm ini config reload docker image nvm" },
      { label: "Store", icon: Package, path: "/store", description: "Engines and runtimes the panel installs for this host", keywords: "install engine appsec open-appsec agent firewall" },
    ],
  },
  {
    label: "Server",
    items: [
      { label: "Firewall", icon: ShieldCheck, path: "/firewall", description: "Open ports and rules", keywords: "iptables ports" },
      { label: "SSH", icon: ShieldAlert, path: "/ssh", description: "SSH login attempts, failed and succeeded", keywords: "sshd auth login security brute force attempts journal" },
      { label: "Cron", icon: CalendarClock, path: "/cron", description: "Scheduled jobs and their run history", keywords: "schedule" },
      { label: "Notifications", icon: Bell, path: "/notifications", description: "Alerts, channels and the delivery log", keywords: "alerts email webhook telegram discord bell" },
      { label: "Docker", icon: Container, path: "/docker", description: "Containers, images and volumes, with live resource use", keywords: "containers images volumes stats compose" },
      {
        label: "WAF",
        icon: Activity,
        path: "/waf",
        description: "Live traffic, where requests come from, firewall events, the WAF audit, bot blocking and the block page",
        keywords: "traffic requests monitor modsecurity bot block map country sources protection page audit",
      },
      { label: "Logs", icon: ScrollText, path: "/logs", description: "Live and recent log files", keywords: "tail" },
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
