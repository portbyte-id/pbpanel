package settings

import (
	"errors"
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
	}
}
