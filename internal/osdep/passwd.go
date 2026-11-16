package osdep

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type passwdEntry struct {
	name string
	uid  int
	gid  int
}

type groupEntry struct {
	name string
	gid  int
}

// readPasswd parses /etc/passwd. Reading the file rather than calling getent keeps
// EnsureUser's decisions testable and independent of the test host's user database.
func readPasswd(path string) (map[string]passwdEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("osdep: read %s: %w", path, err)
	}
	users := make(map[string]passwdEntry)
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			return nil, fmt.Errorf("osdep: %s:%d: want at least 4 colon-separated fields", path, i+1)
		}
		uid, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("osdep: %s:%d: uid %q is not a number", path, i+1, fields[2])
		}
		gid, err := strconv.Atoi(fields[3])
		if err != nil {
			return nil, fmt.Errorf("osdep: %s:%d: gid %q is not a number", path, i+1, fields[3])
		}
		users[fields[0]] = passwdEntry{name: fields[0], uid: uid, gid: gid}
	}
	return users, nil
}

// readGroups parses /etc/group.
func readGroups(path string) ([]groupEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("osdep: read %s: %w", path, err)
	}
	var groups []groupEntry
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 3 {
			return nil, fmt.Errorf("osdep: %s:%d: want at least 3 colon-separated fields", path, i+1)
		}
		gid, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("osdep: %s:%d: gid %q is not a number", path, i+1, fields[2])
		}
		groups = append(groups, groupEntry{name: fields[0], gid: gid})
	}
	return groups, nil
}
