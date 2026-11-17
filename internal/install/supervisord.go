package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// supervisordUnitName is the unit that runs supervisord. It is independent of
// panel.service: the apps must survive the panel being restarted or upgraded
// (development plan step 5.2).
const supervisordUnitName = "panel-supervisord.service"

const (
	// maxSupervisordBytes bounds the download. The release binaries are about
	// 14 MB; a response far beyond that is not the release.
	maxSupervisordBytes = 64 << 20

	downloadSuffix = ".download"
)

// SupervisordRelease is one pinned supervisord binary: where it comes from and
// the SHA-256 it must have. The checksum is the pin; the URL only says where to
// look, so a replaced release asset is refused instead of installed.
type SupervisordRelease struct {
	Version string
	URL     string
	SHA256  string
}

// supervisordReleases are the pinned binaries of github.com/ochinchina/supervisord
// v0.7.6, by GOARCH. The checksums were computed from the downloaded assets and
// agree with the digests the release page publishes; the release has no
// checksums file of its own.
var supervisordReleases = map[string]SupervisordRelease{
	"amd64": {
		Version: "v0.7.6",
		URL:     "https://github.com/ochinchina/supervisord/releases/download/v0.7.6/supervisord_linux_amd64",
		SHA256:  "4a9bd84e902f605bfb47fe0aa67b2cc98f6fea3ee36a2907415c641bdb3fb9b5",
	},
	"arm64": {
		Version: "v0.7.6",
		URL:     "https://github.com/ochinchina/supervisord/releases/download/v0.7.6/supervisord_linux_arm64",
		SHA256:  "337cd0be0f0bffa3bf0dbdb53464b1aac5922f2c29a667c45aede215b3932c18",
	},
}

// DefaultSupervisordRelease is the pinned release for this machine.
func DefaultSupervisordRelease() (SupervisordRelease, error) {
	rel, ok := supervisordReleases[runtime.GOARCH]
	if !ok {
		return SupervisordRelease{}, fmt.Errorf("no pinned supervisord release for %s; the panel supports amd64 and arm64", runtime.GOARCH)
	}
	return rel, nil
}

// SupervisorDir holds supervisord.conf, its socket and its pid file.
func (l Layout) SupervisorDir() string { return filepath.Join(l.PanelDir(), "supervisor") }

// SupervisordPath is the installed supervisord binary.
func (l Layout) SupervisordPath() string { return filepath.Join(l.BinDir(), "supervisord") }

// SupervisordConfPath is supervisord's main config.
func (l Layout) SupervisordConfPath() string {
	return filepath.Join(l.SupervisorDir(), "supervisord.conf")
}

// SupervisordSocketPath is the XML-RPC socket, mode 0700 and owned by root.
func (l Layout) SupervisordSocketPath() string {
	return filepath.Join(l.SupervisorDir(), "supervisord.sock")
}

// SupervisordConf renders supervisord.conf. The only way in is the Unix socket,
// and only root can open it: supervisord runs programs as other accounts, so a
// writable socket would be a root shell for whoever could reach it. There is no
// [inet_http_server]: the panel never opens a TCP listener for it.
//
// Paths are written with forward slashes: this file is read on Linux.
func SupervisordConf(l Layout) string {
	slash := filepath.ToSlash
	var b strings.Builder
	b.WriteString("; Managed by Server Panel (panel install). Programs live in conf.d, one file per app.\n")
	b.WriteString("[supervisord]\n")
	fmt.Fprintf(&b, "logfile=%s\n", slash(filepath.Join(l.LogsDir(), "supervisord.log")))
	b.WriteString("logfile_maxbytes=10MB\n")
	b.WriteString("logfile_backups=3\n")
	fmt.Fprintf(&b, "pidfile=%s\n", slash(filepath.Join(l.SupervisorDir(), "supervisord.pid")))
	b.WriteString("\n[unix_http_server]\n")
	fmt.Fprintf(&b, "file=%s\n", slash(l.SupervisordSocketPath()))
	b.WriteString("chmod=0700\n")
	b.WriteString("\n[include]\n")
	fmt.Fprintf(&b, "files=%s\n", slash(filepath.Join(l.SupervisorDir(), "conf.d", "*.conf")))
	return b.String()
}

// SupervisordUnitFile renders panel-supervisord.service. It does not depend on
// panel.service in either direction, and it restarts on failure: a supervisor that
// dies takes every app with it.
func SupervisordUnitFile(l Layout) string {
	slash := filepath.ToSlash
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Server Panel process supervisor\n")
	b.WriteString("After=network.target\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s -c %s\n", slash(l.SupervisordPath()), slash(l.SupervisordConfPath()))
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=2s\n")
	b.WriteString("StandardOutput=journal\n")
	b.WriteString("StandardError=journal\n")
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// FetchFunc downloads a pinned release to dest, installing nothing at dest unless
// its checksum matches. Tests substitute it; production uses fetchVerified.
type FetchFunc func(ctx context.Context, rel SupervisordRelease, dest string) error

// defaultFetch refuses anything but https and gives the download five minutes.
func defaultFetch(ctx context.Context, rel SupervisordRelease, dest string) error {
	if !strings.HasPrefix(rel.URL, "https://") {
		return fmt.Errorf("refusing to download %s: only https is allowed", rel.URL)
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	return fetchVerified(ctx, client, rel.URL, rel.SHA256, dest, 0o755)
}

// fetchVerified downloads url into dest+".download", checks its SHA-256 and only
// then renames it to dest. A mismatch removes the download and leaves dest, if
// there was one, exactly as it was.
func fetchVerified(ctx context.Context, client *http.Client, url, wantSHA, dest string, mode os.FileMode) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + downloadSuffix
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxSupervisordBytes+1))
	closeErr := f.Close()
	switch {
	case err != nil:
		_ = os.Remove(tmp)
		return fmt.Errorf("download %s: %w", url, err)
	case closeErr != nil:
		_ = os.Remove(tmp)
		return closeErr
	case n > maxSupervisordBytes:
		_ = os.Remove(tmp)
		return fmt.Errorf("download %s: larger than %d bytes", url, maxSupervisordBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != strings.ToLower(wantSHA) {
		_ = os.Remove(tmp)
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s; nothing was installed", url, got, wantSHA)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// fileSHA256 is the hex SHA-256 of a file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// --- install steps -------------------------------------------------------------

func (s *runState) supervisordRelease() (SupervisordRelease, error) {
	if s.Supervisord != nil {
		return *s.Supervisord, nil
	}
	return DefaultSupervisordRelease()
}

// supervisordBinaryDone is true when the installed binary is the pinned one. A
// file with any other content, a partial download or a replaced binary, is not
// done: it is fetched again.
func (s *runState) supervisordBinaryDone(context.Context) (bool, error) {
	rel, err := s.supervisordRelease()
	if err != nil {
		return false, err
	}
	got, err := fileSHA256(s.Layout.SupervisordPath())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return got == strings.ToLower(rel.SHA256), nil
}

func (s *runState) installSupervisord(ctx context.Context) error {
	rel, err := s.supervisordRelease()
	if err != nil {
		return err
	}
	fetch := s.Fetch
	if fetch == nil {
		fetch = defaultFetch
	}
	printf(s.Out, "    supervisord %s\n", rel.Version)
	if err := fetch(ctx, rel, s.Layout.SupervisordPath()); err != nil {
		return err
	}
	s.supervisordChanged = true
	return nil
}

func (s *runState) supervisordConfDone(context.Context) (bool, error) {
	have, err := os.ReadFile(s.Layout.SupervisordConfPath())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return string(have) == SupervisordConf(s.Layout), nil
}

func (s *runState) writeSupervisordConf(context.Context) error {
	if err := os.MkdirAll(filepath.Join(s.Layout.SupervisorDir(), "conf.d"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.Layout.SupervisordConfPath(), []byte(SupervisordConf(s.Layout)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", s.Layout.SupervisordConfPath(), err)
	}
	s.supervisordChanged = true
	return nil
}

func (s *runState) supervisordUnitPath() string {
	return filepath.Join(filepath.Dir(s.Layout.UnitPath), supervisordUnitName)
}

func (s *runState) supervisordUnitDone(context.Context) (bool, error) {
	have, err := os.ReadFile(s.supervisordUnitPath())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return string(have) == SupervisordUnitFile(s.Layout), nil
}

func (s *runState) writeSupervisordUnit(ctx context.Context) error {
	path := s.supervisordUnitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(SupervisordUnitFile(s.Layout)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	s.supervisordChanged = true
	return s.Runner.Run(ctx, "systemctl", "daemon-reload")
}

func (s *runState) supervisordEnabledDone(ctx context.Context) (bool, error) {
	return s.Runner.Run(ctx, "systemctl", "is-enabled", "--quiet", supervisordUnitName) == nil, nil
}

func (s *runState) enableSupervisord(ctx context.Context) error {
	return s.Runner.Run(ctx, "systemctl", "enable", supervisordUnitName)
}

// supervisordActiveDone is true when supervisord is running and nothing this run
// changed needs it restarted. A restart stops every app, which is why it happens
// only for a changed binary, config or unit and never on a plain re-run.
func (s *runState) supervisordActiveDone(ctx context.Context) (bool, error) {
	if s.supervisordChanged {
		return false, nil
	}
	return s.Runner.Run(ctx, "systemctl", "is-active", "--quiet", supervisordUnitName) == nil, nil
}

func (s *runState) startSupervisord(ctx context.Context) error {
	return s.Runner.Run(ctx, "systemctl", "restart", supervisordUnitName)
}
