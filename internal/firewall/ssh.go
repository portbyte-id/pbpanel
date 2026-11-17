package firewall

import (
	"bufio"
	"bytes"
	"context"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// DefaultSSHPort is used when neither sshd nor its config names a port.
const DefaultSSHPort = 22

const maxIncludeDepth = 8

// Outputter runs a command and returns its stdout. Implementations take an argument
// vector, never a shell line.
type Outputter interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// DetectSSHPorts returns every port sshd listens on, sorted and de-duplicated.
// Order of preference: `sshd -T`, then parsing sshd_config and its includes, then 22.
// root is the filesystem holding /etc/ssh, with paths written without a leading slash.
func DetectSSHPorts(ctx context.Context, run Outputter, root fs.FS) []int {
	if run != nil {
		if out, err := run.Output(ctx, "sshd", "-T"); err == nil {
			if ports := ParseSSHDT(out); len(ports) > 0 {
				return ports
			}
		}
	}
	if ports := parseSSHDConfig(root, "etc/ssh/sshd_config", 0); len(ports) > 0 {
		return normalizePorts(ports)
	}
	return []int{DefaultSSHPort}
}

// ParseSSHDT extracts the `port N` lines from `sshd -T` output.
func ParseSSHDT(out []byte) []int {
	var ports []int
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.EqualFold(f[0], "port") {
			if p, ok := parsePort(f[1]); ok {
				ports = append(ports, p)
			}
		}
	}
	return normalizePorts(ports)
}

// parseSSHDConfig collects Port directives, following Include globs. sshd stops
// honouring Port inside a Match block, so parsing stops there too.
func parseSSHDConfig(root fs.FS, file string, depth int) []int {
	if depth > maxIncludeDepth {
		return nil
	}
	data, err := fs.ReadFile(root, file)
	if err != nil {
		return nil
	}
	var ports []int
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch strings.ToLower(f[0]) {
		case "port":
			if p, ok := parsePort(f[1]); ok {
				ports = append(ports, p)
			}
		case "include":
			for _, pat := range f[1:] {
				if abs, ok := strings.CutPrefix(pat, "/"); ok {
					pat = abs
				} else {
					pat = path.Join("etc/ssh", pat)
				}
				matches, _ := fs.Glob(root, pat)
				sort.Strings(matches)
				for _, m := range matches {
					ports = append(ports, parseSSHDConfig(root, m, depth+1)...)
				}
			}
		case "match":
			return ports
		}
	}
	return ports
}

func parsePort(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, false
	}
	return n, true
}

func normalizePorts(in []int) []int {
	seen := make(map[int]bool, len(in))
	out := make([]int, 0, len(in))
	for _, p := range in {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Ints(out)
	return out
}
