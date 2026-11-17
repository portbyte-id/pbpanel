package fm

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

// Errors a Policy returns. The API maps them to 400 (invalid input) or 403
// (refused by a guardrail); callers branch with errors.Is.
var (
	// ErrInvalidPath is a path that cannot name anything: empty, relative, NUL
	// or not UTF-8.
	ErrInvalidPath = errors.New("invalid path")
	// ErrProtected is a target the hard-deny list covers (decision 21).
	ErrProtected = errors.New("protected path")
	// ErrConfirm is a recursive destructive operation whose confirmation does
	// not match the base name.
	ErrConfirm = errors.New("confirmation does not match")
)

// hardDeny are the directories that may never be the *target* of a delete,
// move, or recursive chmod/chown. Their contents stay editable (plan §3.7).
// "/" is handled separately because path.Base("/") is "/".
var hardDeny = []string{
	"/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib32", "/lib64",
	"/libx32", "/media", "/mnt", "/opt", "/proc", "/root", "/run", "/sbin",
	"/srv", "/sys", "/usr", "/var", "/www",
}

// readOnlyTrees refuse every write, not only destructive ones: the kernel
// exposes these as control surfaces, not as files.
var readOnlyTrees = []string{"/proc", "/sys", "/dev"}

// Policy is the single gate every file operation passes (development plan
// step 3.1). It is a pure value: no filesystem access, so the same table runs
// on any host. Symlink resolution is the caller's job (see FS.resolve), which
// then hands the resolved path back to Check* so a link to /etc is judged as
// /etc.
type Policy struct {
	deny []string
}

// NewPolicy builds the policy for a panel tree rooted at panelDir (for example
// /www/panel). The panel directory itself joins the deny list.
func NewPolicy(panelDir string) Policy {
	deny := append([]string(nil), hardDeny...)
	if p := path.Clean(panelDir); p != "." && p != "/" && path.IsAbs(p) {
		deny = append(deny, p)
	}
	return Policy{deny: deny}
}

// Clean turns a client path into an absolute, canonical one. It rejects what
// cannot be a path before any other rule looks at it. path.Clean collapses
// `//`, `.` and `..`, so `//etc`, `/etc/` and `/x/../etc` all become `/etc`.
func (Policy) Clean(p string) (string, error) {
	switch {
	case p == "":
		return "", fmt.Errorf("%w: empty", ErrInvalidPath)
	case strings.ContainsRune(p, 0):
		return "", fmt.Errorf("%w: contains a NUL byte", ErrInvalidPath)
	case !utf8.ValidString(p):
		return "", fmt.Errorf("%w: not valid UTF-8", ErrInvalidPath)
	case !strings.HasPrefix(p, "/"):
		return "", fmt.Errorf("%w: must be absolute", ErrInvalidPath)
	}
	return path.Clean(p), nil
}

// CheckName validates one path component supplied by a client (a new file
// name, a rename target, an upload name).
func CheckName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return fmt.Errorf("%w: %q is not a file name", ErrInvalidPath, name)
	case strings.ContainsAny(name, "/\x00"):
		return fmt.Errorf("%w: a name cannot contain / or NUL", ErrInvalidPath)
	case !utf8.ValidString(name):
		return fmt.Errorf("%w: not valid UTF-8", ErrInvalidPath)
	case len(name) > 255:
		return fmt.Errorf("%w: name is longer than 255 bytes", ErrInvalidPath)
	}
	return nil
}

// CheckWrite refuses a write to, or under, /proc, /sys and /dev.
func (p Policy) CheckWrite(clean string) error {
	for _, tree := range readOnlyTrees {
		if clean == tree || strings.HasPrefix(clean, tree+"/") {
			return fmt.Errorf("%w: %s is a kernel interface and is read-only here", ErrProtected, tree)
		}
	}
	return nil
}

// CheckDestructive refuses a delete, move or recursive chmod/chown whose
// target is "/" or one of the deny-listed directories themselves. It also
// applies CheckWrite, because every destructive operation writes.
func (p Policy) CheckDestructive(clean string) error {
	if err := p.CheckWrite(clean); err != nil {
		return err
	}
	if clean == "/" {
		return fmt.Errorf("%w: the filesystem root", ErrProtected)
	}
	for _, d := range p.deny {
		if clean == d {
			return fmt.Errorf("%w: %s is a system directory (its contents stay editable)", ErrProtected, d)
		}
	}
	return nil
}

// CheckConfirm requires typed confirmation of a recursive destructive
// operation: the exact base name of the target.
func (Policy) CheckConfirm(clean, confirm string) error {
	if confirm == "" || confirm != path.Base(clean) {
		return fmt.Errorf("%w: type the name %q to confirm", ErrConfirm, path.Base(clean))
	}
	return nil
}

// Within reports whether child is parent or lies under it, on cleaned paths.
func Within(parent, child string) bool {
	if parent == "/" {
		return true
	}
	return child == parent || strings.HasPrefix(child, parent+"/")
}
