// Package runtimes manages Node versions through nvm.
//
// nvm is checked out under /www/panel/nvm at a pinned tag and run only to
// install a version; the panel never sources it into a shell it keeps. That one
// call is the single shell use development plan §1.4 allows (step 5.3): nvm is a
// shell function library, so installing through it needs `bash -c`. The script is
// a constant, the version reaches it as a positional argument (never spliced into
// the script text), and the version string is validated against a strict pattern
// first. Removing a version is a directory removal and needs no shell.
package runtimes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	// NVMTag is the pinned nvm release and NVMCommit the commit it must resolve to.
	// The tag names what to clone; the commit is the pin: git verifies the objects it
	// fetches, and a tag that was moved to another commit is caught by comparing
	// HEAD with NVMCommit after the clone. The clone is shallow.
	NVMTag    = "v0.40.3"
	NVMCommit = "977563e97ddc66facf3a8e31c6cff01d236f09bd"
	NVMRepo   = "https://github.com/nvm-sh/nvm.git"

	// installScript is the one shell script the panel runs. $1 is NVM_DIR and $2
	// the version; both arrive as arguments.
	installScript = `export NVM_DIR="$1"; . "$1/nvm.sh" && nvm install "$2"`
)

var (
	// requestRe is a version an operator may ask for: a major, major.minor or
	// major.minor.patch.
	requestRe = regexp.MustCompile(`^[0-9]{1,2}(\.[0-9]{1,3}){0,2}$`)
	// fullRe is an installed version: exactly x.y.z.
	fullRe = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]{1,3}\.[0-9]{1,3}$`)
)

// ValidRequest reports whether v is a version an operator may ask to install.
func ValidRequest(v string) bool { return requestRe.MatchString(v) }

// ValidFull reports whether v is an exact installed version, x.y.z.
func ValidFull(v string) bool { return fullRe.MatchString(v) }

// Errors the manager returns.
var (
	ErrInvalidVersion = errors.New("runtimes: use a version such as 22, 22.11 or 22.11.0")
	ErrNotInstalled   = errors.New("runtimes: that Node.js version is not installed")
	ErrInUse          = errors.New("runtimes: apps still run on that Node.js version")
	ErrBusy           = errors.New("runtimes: another install or removal is running")
)

// Paths is where nvm and its Node versions live. Built with package path for the
// same reason as nginx.Paths: the strings are written into config files.
type Paths struct {
	Root string
}

// NVMDir is the nvm checkout and NVM_DIR.
func (p Paths) NVMDir() string { return path.Join(p.Root, "panel", "nvm") }

// VersionsDir holds one directory per installed version.
func (p Paths) VersionsDir() string { return path.Join(p.NVMDir(), "versions", "node") }

// NodeDir is one installed version.
func (p Paths) NodeDir(full string) string { return path.Join(p.VersionsDir(), "v"+full) }

// NodeBin is the absolute path of that version's node binary.
func (p Paths) NodeBin(full string) string { return path.Join(p.NodeDir(full), "bin", "node") }

// Exec runs one command, delivering its combined output line by line.
type Exec interface {
	Run(ctx context.Context, argv []string, env []string, onLine func(string)) error
}

// ExecCmd is the production Exec.
type ExecCmd struct{}

// Run implements Exec.
func (ExecCmd) Run(ctx context.Context, argv []string, env []string, onLine func(string)) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	w := &lineWriter{emit: onLine}
	cmd.Stdout, cmd.Stderr = w, w
	err := cmd.Run()
	w.flush()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.Join(argv[:min(len(argv), 2)], " "), err)
	}
	return nil
}

// lineWriter splits output at \n and \r, so nvm's progress meter becomes lines.
type lineWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	emit func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range p {
		if b == '\n' || b == '\r' {
			w.flushLocked()
			continue
		}
		w.buf.WriteByte(b)
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked()
}

func (w *lineWriter) flushLocked() {
	if w.buf.Len() == 0 {
		return
	}
	if w.emit != nil {
		w.emit(w.buf.String())
	}
	w.buf.Reset()
}

// Options configures a Manager.
type Options struct {
	Paths Paths
	// Exec runs git and bash; nil means ExecCmd.
	Exec Exec
	// Users lists the apps that run on each full version. Nil means none do.
	Users func(ctx context.Context) (map[string][]string, error)
}

// Manager installs and removes Node versions. One install or removal runs at a
// time: nvm shares a cache and a checkout.
type Manager struct {
	p     Paths
	ex    Exec
	users func(ctx context.Context) (map[string][]string, error)
	busy  sync.Mutex
}

// New returns a Manager.
func New(o Options) *Manager {
	m := &Manager{p: o.Paths, ex: o.Exec, users: o.Users}
	if m.ex == nil {
		m.ex = ExecCmd{}
	}
	return m
}

// Node is one installed version.
type Node struct {
	Version string   `json:"version"`
	Path    string   `json:"path"`
	UsedBy  []string `json:"used_by"`
}

// State is what the Runtimes page shows.
type State struct {
	NVMInstalled bool   `json:"nvm_installed"`
	NVMTag       string `json:"nvm_tag"`
	Node         []Node `json:"node"`
}

// installed lists the full versions on disk, highest first. A directory without a
// node binary is a half-finished install and is not listed.
func (m *Manager) installed() []string {
	entries, err := os.ReadDir(filepath.FromSlash(m.p.VersionsDir()))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		full := strings.TrimPrefix(e.Name(), "v")
		if !e.IsDir() || !fullRe.MatchString(full) {
			continue
		}
		if _, err := os.Stat(filepath.FromSlash(m.p.NodeBin(full))); err != nil {
			continue
		}
		out = append(out, full)
	}
	sort.Slice(out, func(i, j int) bool { return compare(out[i], out[j]) > 0 })
	return out
}

// compare orders two x.y.z versions numerically.
func compare(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x - y
		}
	}
	return len(as) - len(bs)
}

// Resolve maps a requested version (22, 22.11) to the highest installed version
// it matches.
func (m *Manager) Resolve(request string) (string, bool) {
	if !requestRe.MatchString(request) {
		return "", false
	}
	want := strings.Split(request, ".")
	for _, full := range m.installed() { // highest first
		have := strings.Split(full, ".")
		match := true
		for i := range want {
			if want[i] != have[i] {
				match = false
				break
			}
		}
		if match {
			return full, true
		}
	}
	return "", false
}

// NodePath is the absolute node binary a request resolves to. It is the apps
// manager's NodeResolver.
func (m *Manager) NodePath(request string) (string, error) {
	if !requestRe.MatchString(request) {
		return "", ErrInvalidVersion
	}
	full, ok := m.Resolve(request)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotInstalled, request)
	}
	return m.p.NodeBin(full), nil
}

// List reports nvm and every installed version with the apps that use it.
func (m *Manager) List(ctx context.Context) (State, error) {
	st := State{NVMTag: NVMTag, Node: []Node{}}
	if _, err := os.Stat(filepath.FromSlash(path.Join(m.p.NVMDir(), "nvm.sh"))); err == nil {
		st.NVMInstalled = true
	}
	var used map[string][]string
	if m.users != nil {
		var err error
		if used, err = m.users(ctx); err != nil {
			return st, err
		}
	}
	for _, full := range m.installed() {
		u := used[full]
		if u == nil {
			u = []string{}
		}
		st.Node = append(st.Node, Node{Version: full, Path: m.p.NodeBin(full), UsedBy: u})
	}
	return st, nil
}

// Install installs a Node version, fetching nvm first when it is not there. Its
// output is delivered line by line, for a job's log.
func (m *Manager) Install(ctx context.Context, version string, onLine func(string)) error {
	if !requestRe.MatchString(version) {
		return ErrInvalidVersion
	}
	if !m.busy.TryLock() {
		return ErrBusy
	}
	defer m.busy.Unlock()

	dir := filepath.FromSlash(m.p.NVMDir())
	if _, err := os.Stat(filepath.Join(dir, "nvm.sh")); errors.Is(err, fs.ErrNotExist) {
		onLine("fetching nvm " + NVMTag)
		clone := []string{"git", "clone", "--depth", "1", "--branch", NVMTag, NVMRepo, m.p.NVMDir()}
		if err := m.ex.Run(ctx, clone, nil, onLine); err != nil {
			return fmt.Errorf("fetch nvm (git must be installed): %w", err)
		}
		var head string
		revParse := []string{"git", "-C", m.p.NVMDir(), "rev-parse", "HEAD"}
		if err := m.ex.Run(ctx, revParse, nil, func(l string) {
			if t := strings.TrimSpace(l); t != "" {
				head = t
			}
		}); err != nil {
			return fmt.Errorf("check the nvm checkout: %w", err)
		}
		if head != NVMCommit {
			// Not removed to be tidy: a checkout that is not the pinned commit must
			// never be sourced, and nvm.sh being absent is what stops that.
			_ = os.RemoveAll(dir)
			return fmt.Errorf("nvm %s resolved to commit %s, not the pinned %s; the checkout was removed", NVMTag, head, NVMCommit)
		}
	}
	onLine("installing Node.js " + version)
	run := []string{"bash", "-c", installScript, "bash", m.p.NVMDir(), version}
	if err := m.ex.Run(ctx, run, []string{"NVM_DIR=" + m.p.NVMDir()}, onLine); err != nil {
		return fmt.Errorf("nvm install %s: %w", version, err)
	}
	full, ok := m.Resolve(version)
	if !ok {
		return fmt.Errorf("nvm finished but no Node.js %s is installed under %s", version, m.p.VersionsDir())
	}
	onLine("installed Node.js " + full)
	return nil
}

// Uninstall removes one installed version. It refuses while an app runs on it.
func (m *Manager) Uninstall(ctx context.Context, full string, onLine func(string)) error {
	if !fullRe.MatchString(full) {
		return ErrInvalidVersion
	}
	if !m.busy.TryLock() {
		return ErrBusy
	}
	defer m.busy.Unlock()

	if _, err := os.Stat(filepath.FromSlash(m.p.NodeBin(full))); err != nil {
		return fmt.Errorf("%w: %s", ErrNotInstalled, full)
	}
	if m.users != nil {
		used, err := m.users(ctx)
		if err != nil {
			return err
		}
		if names := used[full]; len(names) > 0 {
			return fmt.Errorf("%w: %s", ErrInUse, strings.Join(names, ", "))
		}
	}
	onLine("removing Node.js " + full)
	if err := os.RemoveAll(filepath.FromSlash(m.p.NodeDir(full))); err != nil {
		return fmt.Errorf("remove Node.js %s: %w", full, err)
	}
	onLine("removed Node.js " + full)
	return nil
}
