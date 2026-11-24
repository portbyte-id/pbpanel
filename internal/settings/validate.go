package settings

import (
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/waf"
)

// validate runs the type check, then the key's own validator. The returned
// error is a *ValidationError carrying the message the operator will see.
func validate(k key, value string) error {
	if err := validateType(k.meta, value); err != nil {
		return &ValidationError{Name: k.meta.Name, Value: value, Reason: err.Error()}
	}
	if k.check == nil {
		return nil
	}
	if err := k.check(value); err != nil {
		return &ValidationError{Name: k.meta.Name, Value: value, Reason: err.Error()}
	}
	return nil
}

func validateType(m Meta, value string) error {
	switch m.Type {
	case TypeInt:
		return validateInt(value)
	case TypeString:
		if value == "" && !m.Optional {
			return fmt.Errorf("must not be empty")
		}
		return nil
	case TypeBool:
		return validateBool(value)
	case TypeDuration:
		return validateDuration(value)
	case TypePort:
		return validatePort(value)
	case TypePath:
		return validatePath(value)
	case TypeEnum:
		if len(m.Allowed) == 0 {
			return fmt.Errorf("enum key has no allowed values")
		}
		if !slices.Contains(m.Allowed, value) {
			return fmt.Errorf("must be one of: %s", strings.Join(m.Allowed, ", "))
		}
		return nil
	default:
		return fmt.Errorf("unknown type %q", m.Type)
	}
}

// Integers are stored in one spelling only: Set rejects "+5" and "007" so a
// value read back always equals the value that was written.
func validateInt(value string) error {
	n, err := strconv.Atoi(value)
	if err != nil || strconv.Itoa(n) != value {
		return fmt.Errorf("must be a whole number")
	}
	return nil
}

func validateBool(value string) error {
	if value != "true" && value != "false" {
		return fmt.Errorf("must be true or false")
	}
	return nil
}

func validateDuration(value string) error {
	if value == "" {
		return fmt.Errorf("must be a duration such as 30m or 1h")
	}
	if _, err := time.ParseDuration(value); err != nil {
		return fmt.Errorf("must be a duration such as 30m or 1h")
	}
	return nil
}

func validatePort(value string) error {
	n, err := strconv.Atoi(value)
	if err != nil || strconv.Itoa(n) != value || n < 1 || n > 65535 {
		return fmt.Errorf("must be a port number between 1 and 65535")
	}
	return nil
}

// Paths are stored cleaned and absolute. The panel's paths are Linux paths
// (`/www/panel`), so this uses POSIX rules even when the tests run on Windows,
// where filepath.IsAbs would call "/www/panel" relative.
func validatePath(value string) error {
	if value == "" {
		return fmt.Errorf("must be an absolute path")
	}
	if !path.IsAbs(value) {
		return fmt.Errorf("must be an absolute path")
	}
	if path.Clean(value) != value {
		return fmt.Errorf("must be a clean path, without '.' or '..' or a trailing separator")
	}
	return nil
}

// validateOptionalTurnstileSiteKey and validateOptionalTurnstileSecret accept an
// empty value, which means Turnstile is not configured.
func validateOptionalTurnstileSiteKey(value string) error {
	if value == "" || waf.ValidTurnstileSiteKey(value) {
		return nil
	}
	return fmt.Errorf("must be letters, digits, _ or - (up to %d characters)", waf.MaxTurnstileKey)
}

func validateOptionalTurnstileSecret(value string) error {
	if value == "" || waf.ValidTurnstileSecret(value) {
		return nil
	}
	return fmt.Errorf("must be the secret key from the Cloudflare dashboard: letters, digits, _ and -")
}

func validateAddr(value string) error {
	if _, err := netip.ParseAddr(value); err != nil {
		return fmt.Errorf("must be an IP address such as 0.0.0.0 or 127.0.0.1")
	}
	return nil
}

// bindingLabelRe is one DNS label: letters, digits and inner hyphens, at most 63
// characters.
var bindingLabelRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// validateBindingDomain accepts "" (no binding) or a host name with at least two
// labels. An IP address is refused: the binding is about a name the operator
// owns, and an address would not survive a change of network.
func validateBindingDomain(value string) error {
	if value == "" {
		return nil
	}
	if _, err := netip.ParseAddr(value); err == nil || len(value) > 253 {
		return fmt.Errorf("must be a domain name such as panel.example.com, not an IP address")
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return fmt.Errorf("must be a domain name with at least two parts, such as panel.example.com")
	}
	for _, l := range labels {
		if !bindingLabelRe.MatchString(l) {
			return fmt.Errorf("%q is not a valid domain name", value)
		}
	}
	return nil
}

func rangeInt(min, max int) func(string) error {
	return func(value string) error {
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("must be a whole number")
		}
		if n < min || n > max {
			return fmt.Errorf("must be between %d and %d", min, max)
		}
		return nil
	}
}

func positiveDuration(value string) error {
	d, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("must be a duration such as 30m or 1h")
	}
	if d <= 0 {
		return fmt.Errorf("must be greater than zero")
	}
	return nil
}

// checkKey is the boot-time guarantee: every registered key must have metadata
// the Settings page can render and a default its own validator accepts.
func checkKey(k key) error {
	name := k.meta.Name
	switch {
	case name == "":
		return fmt.Errorf("settings: a key has no name")
	case k.meta.Description == "":
		return fmt.Errorf("settings: key %s has no description", name)
	case k.meta.Group == "":
		return fmt.Errorf("settings: key %s has no group", name)
	case k.meta.Default == "" && !k.meta.Optional:
		return fmt.Errorf("settings: key %s has no default", name)
	}
	if err := validate(k, k.meta.Default); err != nil {
		return fmt.Errorf("settings: key %s has an invalid default %q: %w", name, k.meta.Default, err)
	}
	return nil
}
