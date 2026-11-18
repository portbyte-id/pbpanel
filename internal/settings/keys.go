package settings

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Groups shown on the Settings page, in this order.
const (
	groupPanel   = "Panel"
	groupLogin   = "Login"
	groupSession = "Session"
	groupPHP     = "PHP"
	groupFiles   = "Files"
	groupTLS     = "TLS"
	groupCron    = "Cron"
	groupLogs    = "Logs"
)

// Cron and log settings (development plan M7).
const (
	// KeyCronAllowShell permits cron jobs of kind "shell". It is read on every
	// create, update and run, so it applies without a restart.
	KeyCronAllowShell = "cron.allow_shell"
	// KeyLogsRotateMaxBytes is the size at which a panel-owned log is rotated.
	KeyLogsRotateMaxBytes = "logs.rotate.max_bytes"
	// KeyLogsRotateKeep is how many rotated files are kept per log.
	KeyLogsRotateKeep = "logs.rotate.keep"
)

// File manager settings (development plan M3). Sizes are bytes.
const (
	KeyFMEditorMaxBytes     = "fm.editor.max_bytes"
	KeyFMChunkMaxBytes      = "fm.upload.chunk_max_bytes"
	KeyFMUploadSweepAge     = "fm.upload.sweep_age"
	KeyFMTrashMaxBytes      = "fm.trash.max_bytes"
	KeyFMTrashRetentionDays = "fm.trash.retention_days"
	KeyFMArchiveMaxBytes    = "fm.archive.max_bytes"
	KeyFMArchiveMaxEntries  = "fm.archive.max_entries"
)

// TLS settings (development plan M6). The certificate issuer and the renewal
// job read them; the defaults are chosen so a fresh install cannot burn the
// production rate limits.
const (
	// KeyTLSACMEDirectory is the ACME directory URL new orders go to.
	KeyTLSACMEDirectory = "tls.acme.directory"
	// KeyTLSRenewDaysBefore is how many days before expiry a certificate is renewed.
	KeyTLSRenewDaysBefore = "tls.renew.days_before"
	// KeyTLSPanelAutoIP turns on the automatic IP-address certificate for the
	// panel's own listener on a host with a public IP.
	KeyTLSPanelAutoIP = "tls.panel.auto_ip"

	// DefaultACMEDirectory is Let's Encrypt staging. Staging certificates are
	// not trusted by browsers; switching to production is a deliberate act.
	DefaultACMEDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

func validateACMEDirectory(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("must be an https URL such as " + DefaultACMEDirectory)
	}
	return nil
}

// KeyPHPVersions names the versions offered on the PHP page.
const KeyPHPVersions = "php.versions"

var phpVersionsRe = regexp.MustCompile(`^[0-9]\.[0-9]{1,2}(\s*,\s*[0-9]\.[0-9]{1,2})*$`)

func validatePHPVersions(value string) error {
	if !phpVersionsRe.MatchString(strings.TrimSpace(value)) {
		return errors.New("must be PHP versions separated by commas, for example 8.3,8.4")
	}
	return nil
}

// defaultKeys is the panel's key table. Every key here is a setting the panel
// actually reads; a key nobody reads is a dead setting on the Settings page.
func defaultKeys() []key {
	return []key{
		{
			meta: Meta{
				Name:            "panel.port",
				Type:            TypePort,
				Default:         "8443",
				RestartRequired: true,
				Description:     "HTTPS port the panel listens on.",
				Group:           groupPanel,
			},
		},
		{
			meta: Meta{
				Name:            "panel.bind",
				Type:            TypeString,
				Default:         "0.0.0.0",
				RestartRequired: true,
				Description:     "Address the panel listens on. Use 127.0.0.1 to keep it off the network.",
				Group:           groupPanel,
			},
			check: validateAddr,
		},
		{
			meta: Meta{
				Name:        "login.max_attempts",
				Type:        TypeInt,
				Default:     "5",
				Description: "Failed logins allowed from one address before it is locked out.",
				Group:       groupLogin,
			},
			check: rangeInt(1, 1000),
		},
		{
			meta: Meta{
				Name:        "login.lockout",
				Type:        TypeDuration,
				Default:     "15m",
				Description: "How long an address stays locked out after too many failed logins.",
				Group:       groupLogin,
			},
			check: positiveDuration,
		},
		{
			meta: Meta{
				Name:        "session.idle_timeout",
				Type:        TypeDuration,
				Default:     "30m",
				Description: "A session with no request for this long is logged out.",
				Group:       groupSession,
			},
			check: positiveDuration,
		},
		{
			meta: Meta{
				Name:        KeyPHPVersions,
				Type:        TypeString,
				Default:     "8.2,8.3,8.4,8.5",
				Description: "PHP versions offered on the PHP page, separated by commas. Each needs an official php:<version>-fpm image.",
				Group:       groupPHP,
			},
			check: validatePHPVersions,
		},
		{
			meta: Meta{
				Name:        KeyFMEditorMaxBytes,
				Type:        TypeInt,
				Default:     "2097152",
				Description: "Largest file, in bytes, the text editor opens or saves.",
				Group:       groupFiles,
			},
			check: rangeInt(1024, 256<<20),
		},
		{
			meta: Meta{
				Name:        KeyFMChunkMaxBytes,
				Type:        TypeInt,
				Default:     "8388608",
				Description: "Largest upload chunk, in bytes, the server accepts. The browser sends smaller ones.",
				Group:       groupFiles,
			},
			check: rangeInt(64<<10, 64<<20),
		},
		{
			meta: Meta{
				Name:        KeyFMUploadSweepAge,
				Type:        TypeDuration,
				Default:     "24h",
				Description: "An unfinished upload idle for this long is deleted.",
				Group:       groupFiles,
			},
			check: positiveDuration,
		},
		{
			meta: Meta{
				Name:        KeyFMTrashMaxBytes,
				Type:        TypeInt,
				Default:     "10737418240",
				Description: "Size limit of the recycle bin in bytes. The oldest entries are purged past it, and one item larger than this must be deleted permanently. 0 means no limit.",
				Group:       groupFiles,
			},
			check: rangeInt(0, 1<<50),
		},
		{
			meta: Meta{
				Name:        KeyFMTrashRetentionDays,
				Type:        TypeInt,
				Default:     "30",
				Description: "Days a deleted item stays in the recycle bin before the daily purge removes it. 0 keeps items until the size limit evicts them.",
				Group:       groupFiles,
			},
			check: rangeInt(0, 3650),
		},
		{
			meta: Meta{
				Name:        KeyFMArchiveMaxBytes,
				Type:        TypeInt,
				Default:     "21474836480",
				Description: "Most bytes one archive extraction may write. Guards against decompression bombs.",
				Group:       groupFiles,
			},
			check: rangeInt(1<<20, 1<<50),
		},
		{
			meta: Meta{
				Name:        KeyFMArchiveMaxEntries,
				Type:        TypeInt,
				Default:     "200000",
				Description: "Most entries one archive extraction may contain.",
				Group:       groupFiles,
			},
			check: rangeInt(1, 10_000_000),
		},
		{
			meta: Meta{
				Name:        KeyTLSACMEDirectory,
				Type:        TypeString,
				Default:     DefaultACMEDirectory,
				Description: "ACME directory URL certificates are ordered from. The default is the Let's Encrypt staging endpoint, whose certificates browsers do not trust; set the production URL to issue real ones.",
				Group:       groupTLS,
			},
			check: validateACMEDirectory,
		},
		{
			meta: Meta{
				Name:        KeyTLSRenewDaysBefore,
				Type:        TypeInt,
				Default:     "30",
				Description: "A certificate is renewed when it has this many days or fewer left.",
				Group:       groupTLS,
			},
			check: rangeInt(1, 60),
		},
		{
			meta: Meta{
				Name:        KeyTLSPanelAutoIP,
				Type:        TypeBool,
				Default:     "true",
				Description: "When this host has a public IPv4 address on one of its own interfaces (not behind NAT), request an IP-address certificate for the panel from the ACME directory and keep it renewed. It never replaces a certificate you uploaded or one issued for a host name. Port 80 must be reachable from the internet.",
				Group:       groupTLS,
			},
		},
		{
			meta: Meta{
				Name:        KeyCronAllowShell,
				Type:        TypeBool,
				Default:     "false",
				Description: "Allow cron jobs that run a shell script. Off by default: a shell job runs whatever text it holds as the account it names. Argv and script-file jobs are always allowed.",
				Group:       groupCron,
			},
		},
		{
			meta: Meta{
				Name:            KeyLogsRotateMaxBytes,
				Type:            TypeInt,
				Default:         "52428800",
				RestartRequired: true,
				Description:     "A panel-owned log (site, audit, supervisor) is rotated once it reaches this many bytes. Checked hourly.",
				Group:           groupLogs,
			},
			check: rangeInt(1<<20, 1<<40),
		},
		{
			meta: Meta{
				Name:            KeyLogsRotateKeep,
				Type:            TypeInt,
				Default:         "7",
				RestartRequired: true,
				Description:     "Rotated files kept per log; the oldest is deleted when a new one is made.",
				Group:           groupLogs,
			},
			check: rangeInt(1, 365),
		},
	}
}
