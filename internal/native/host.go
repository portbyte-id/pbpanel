package native

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Host is the part of /etc/os-release a build is matched on.
type Host struct {
	ID        string // ubuntu, debian, rocky
	VersionID string // 26.04, 12, 9.8
}

// ReadHost parses an os-release file.
func ReadHost(file, product string) (Host, error) {
	f, err := os.Open(file)
	if err != nil {
		return Host{}, fmt.Errorf("%s: read the host release: %w", product, err)
	}
	defer func() { _ = f.Close() }()
	return ParseHost(f, product)
}

// ParseHost parses os-release content.
func ParseHost(r io.Reader, product string) (Host, error) {
	var h Host
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			h.ID = v
		case "VERSION_ID":
			h.VersionID = v
		}
	}
	if err := sc.Err(); err != nil {
		return Host{}, err
	}
	if h.ID == "" || h.VersionID == "" {
		return Host{}, fmt.Errorf("%s: the host release names no ID or VERSION_ID", product)
	}
	return h, nil
}

// ErrUnsupportedHost is a host the builder cannot match yet.
var ErrUnsupportedHost = errors.New("no builder image matches this host")

// BuilderImage is the image a build compiles on: the same distro and release as
// the host, so the binary links the host's own glibc. The EL family is not built
// yet (development plan M9: it needs a dnf builder and SELinux labels).
func BuilderImage(h Host, product string) (string, error) {
	switch h.ID {
	case "ubuntu", "debian":
		return h.ID + ":" + h.VersionID, nil
	}
	return "", fmt.Errorf("%s: %w: %s %s is not supported yet", product, ErrUnsupportedHost, h.ID, h.VersionID)
}
