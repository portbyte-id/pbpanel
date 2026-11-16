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
	}
}
