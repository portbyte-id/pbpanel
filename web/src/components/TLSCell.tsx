import { Badge } from "@/components/ui/badge"
import type { CertView } from "@/lib/api"

/** HTTPS state for a list row: expiry soon is a warning, not just a number. */
export function TLSCell({ cert }: { cert: CertView | undefined }) {
  if (!cert) return <span className="text-muted-foreground">No HTTPS</span>
  if (cert.days_left < 0) return <Badge variant="destructive">Expired</Badge>
  if (cert.days_left <= 14) return <Badge variant="warning">Expires in {cert.days_left}d</Badge>
  return <Badge variant="success">HTTPS · {cert.days_left}d</Badge>
}
