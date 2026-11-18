/** Types and display helpers for the Docker page. The shapes mirror internal/docker/manage.go. */

export interface DockerInfo {
  serverVersion: string
  operatingSystem: string
  kernelVersion: string
  storageDriver: string
  rootDir: string
  cpus: number
  memTotal: number
  containers: number
  running: number
  paused: number
  stopped: number
  images: number
}

export interface DockerPort {
  ip?: string
  privatePort: number
  publicPort?: number
  type: string
}

export interface DockerContainer {
  id: string
  name: string
  image: string
  imageId: string
  command: string
  created: string
  state: string
  status: string
  ports: DockerPort[]
  labels: Record<string, string>
  volumes: string[]
  /** Run by the panel itself (nginx, PHP-FPM): it cannot be stopped or removed here. */
  managed: boolean
}

export interface DockerStats {
  id: string
  name: string
  cpuPercent: number
  memUsage: number
  memLimit: number
  memPercent: number
  netRx: number
  netTx: number
  blockRead: number
  blockWrite: number
  pids: number
}

export interface DockerProcesses {
  titles: string[]
  processes: string[][]
}

export interface DockerImage {
  id: string
  tags: string[]
  digests: string[]
  size: number
  created: string
  dangling: boolean
  inUseBy: string[]
  managed: boolean
}

export interface DockerVolume {
  name: string
  driver: string
  mountpoint: string
  scope: string
  createdAt: string
  labels: Record<string, string>
  inUseBy: string[]
}

export interface DockerLogs {
  logs: string
  truncated: boolean
}

export type DockerAction = "start" | "stop" | "restart" | "kill"

export const ACTION_LABELS: Record<DockerAction, string> = {
  start: "Start",
  stop: "Stop",
  restart: "Restart",
  kill: "Kill",
}

type BadgeTone = "success" | "warning" | "destructive" | "outline"

/** The badge tone for a container state: running is good, a crash loop is not. */
export function stateTone(state: string): BadgeTone {
  switch (state) {
    case "running":
      return "success"
    case "restarting":
    case "paused":
      return "warning"
    case "dead":
      return "destructive"
    default:
      return "outline"
  }
}

/** `8080→80/tcp`, or `80/tcp` for a port that is not published. */
export function portLabel(p: DockerPort): string {
  if (p.publicPort) {
    const host = p.ip && p.ip !== "0.0.0.0" && p.ip !== "::" ? `${p.ip}:` : ""
    return `${host}${p.publicPort}→${p.privatePort}/${p.type}`
  }
  return `${p.privatePort}/${p.type}`
}

/** Collapses the IPv4 and IPv6 rows Docker reports for one published port. */
export function uniquePorts(ports: DockerPort[]): DockerPort[] {
  const seen = new Set<string>()
  const out: DockerPort[] = []
  for (const p of ports) {
    const label = portLabel(p)
    if (seen.has(label)) continue
    seen.add(label)
    out.push(p)
  }
  return out
}

export function formatPercent(value: number): string {
  if (!Number.isFinite(value)) return "–"
  return `${value.toFixed(value >= 100 ? 0 : 1)}%`
}

/** `abc123def456` from a full `sha256:` image ID or a 64-character container ID. */
export function shortId(id: string): string {
  return id.replace(/^sha256:/, "").slice(0, 12)
}

/** The tag to show for an image: its first tag, or its short ID when it has none. */
export function imageLabel(image: Pick<DockerImage, "id" | "tags">): string {
  return image.tags[0] ?? `<none> ${shortId(image.id)}`
}
