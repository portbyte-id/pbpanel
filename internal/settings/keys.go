package settings

// Groups shown on the Settings page, in this order.
const (
	groupPanel   = "Panel"
	groupLogin   = "Login"
	groupSession = "Session"
)

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
	}
}
