package osdep

import (
	"fmt"
	"strings"
)

// parseOSRelease reads the key=value format of /etc/os-release (and of
// /usr/lib/os-release). Values may be single- or double-quoted; unquoted values
// have no whitespace by definition, so quotes are simply stripped.
func parseOSRelease(data []byte) (map[string]string, error) {
	values := make(map[string]string)
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: %q is not key=value", i+1, line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key", i+1)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
	return values, nil
}
