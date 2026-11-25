package notify

import (
	"regexp"
	"sort"
)

// Known kinds. Owners are listed in docs/design/notifications-plan.md §4.1.
const (
	KindTLSRenewFailed     = "tls.renew_failed"
	KindTLSExpiring        = "tls.expiring"
	KindJobFailed          = "job.failed"
	KindCronFailed         = "cron.failed"
	KindFirewallDisabled   = "firewall.disabled"
	KindSiteDetached       = "site.detached"
	KindNotifyDeliveryFail = "notify.delivery_failed"
	KindNotifyTest         = "notify.test"
	KindBackupFailed       = "backup.failed"
	KindBackupSucceeded    = "backup.succeeded"
	KindBackupVerifyFailed = "backup.verify_failed"
	KindMetricsThreshold   = "metrics.threshold"
	KindBanAdded           = "ban.added"
	KindBanCapHit          = "ban.cap_hit"
	KindSecurityFinding    = "security.finding"
	KindIntegrityChanged   = "integrity.changed"
	KindConfigApplied      = "config.applied"
	KindConfigApplyFailed  = "config.apply_failed"
)

// defaultSeverityUnknown is what a kind outside the registry gets when the
// producer gives no severity. Unknown kinds are accepted (D11) and shown as-is.
const defaultSeverityUnknown = Warning

// KindInfo describes one known kind for the filter UI and for defaults.
type KindInfo struct {
	Kind     string   `json:"kind"`
	Severity Severity `json:"severity"`
	Label    string   `json:"label"`
}

// registry is the set of known kinds. Keep it in step with the table in
// docs/design/notifications-plan.md §4.1 and the roadmap's event kinds table.
var registry = map[string]KindInfo{
	KindTLSRenewFailed:     {KindTLSRenewFailed, Critical, "Certificate renewal failed"},
	KindTLSExpiring:        {KindTLSExpiring, Warning, "Certificate expiring"},
	KindJobFailed:          {KindJobFailed, Warning, "Background job failed"},
	KindCronFailed:         {KindCronFailed, Warning, "Scheduled job failed"},
	KindFirewallDisabled:   {KindFirewallDisabled, Warning, "Firewall disabled"},
	KindSiteDetached:       {KindSiteDetached, Warning, "Site features detached"},
	KindNotifyDeliveryFail: {KindNotifyDeliveryFail, Warning, "Notification delivery failed"},
	KindNotifyTest:         {KindNotifyTest, Info, "Test notification"},
	KindBackupFailed:       {KindBackupFailed, Critical, "Backup failed"},
	KindBackupSucceeded:    {KindBackupSucceeded, Info, "Backup succeeded"},
	KindBackupVerifyFailed: {KindBackupVerifyFailed, Critical, "Backup verification failed"},
	KindMetricsThreshold:   {KindMetricsThreshold, Warning, "Metric threshold"},
	KindBanAdded:           {KindBanAdded, Info, "IP address banned"},
	KindBanCapHit:          {KindBanCapHit, Warning, "Ban limit reached"},
	KindSecurityFinding:    {KindSecurityFinding, Warning, "Security finding"},
	KindIntegrityChanged:   {KindIntegrityChanged, Warning, "Integrity changed"},
	KindConfigApplied:      {KindConfigApplied, Info, "Configuration applied"},
	KindConfigApplyFailed:  {KindConfigApplyFailed, Warning, "Configuration apply failed"},
}

// kindPattern is the naming rule: "<area>.<event>", lowercase, one dot.
var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*$`)

// maxKindLen is the byte cap on a kind.
const maxKindLen = 48

// ValidKind reports whether kind is well formed. It does not require the kind
// to be known: unknown well-formed kinds are accepted (D11).
func ValidKind(kind string) bool {
	return len(kind) <= maxKindLen && kindPattern.MatchString(kind)
}

// Known reports whether kind is in the registry.
func Known(kind string) bool {
	_, ok := registry[kind]
	return ok
}

// DefaultSeverity returns the severity an event of kind gets when the producer
// gives none. Unknown kinds default to warning.
func DefaultSeverity(kind string) Severity {
	if info, ok := registry[kind]; ok {
		return info.Severity
	}
	return defaultSeverityUnknown
}

// Kinds returns the known kinds sorted by name, for the filter UI.
func Kinds() []KindInfo {
	out := make([]KindInfo, 0, len(registry))
	for _, info := range registry {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}
