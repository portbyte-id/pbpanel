package install

import (
	"os"
	"path/filepath"
)

const (
	// DefaultRoot is the install root from plan.md §2.
	DefaultRoot = "/www"

	// DefaultUnitPath is the only file install writes outside the root
	// (plan.md §2, "Outside /www").
	DefaultUnitPath = "/etc/systemd/system/panel.service"

	// BinaryName is the installed executable's file name.
	BinaryName = "pbp"

	// DefaultLinkPath is the name on PATH: a symlink to BinaryPath so the
	// operator types `pbp` rather than the full install path. A symlink rather
	// than a copy, so an upgrade replaces the target and the PATH entry never
	// goes stale holding an old binary.
	DefaultLinkPath = "/usr/local/bin/" + BinaryName

	// WWWUser is the one shared account nginx workers, PHP pools and apps run
	// as (decision 23).
	WWWUser = "www"

	// WWWUID and WWWGID are fixed rather than allocated. They travel in file
	// ownership and in container bind mounts, so a host that picked different
	// numbers on a second install would leave every file the first install
	// created owned by nobody.
	//
	// 1500 is above Ubuntu's UID_MIN (1000), so no distribution package claims
	// it; a human account can, and EnsureUser refuses loudly when one does
	// rather than moving files that already carry those numbers.
	WWWUID = 1500
	WWWGID = 1500

	// secretKeyBytes is the size of the random key install writes to
	// data/secret.key. 32 bytes is the output size of the hash it keys.
	secretKeyBytes = 32

	// fileModeSecret is for data/secret.key: the key decrypts nothing by
	// itself yet, but it is a secret and is treated as one from the start.
	fileModeSecret os.FileMode = 0o600
)

// dirMode is one directory's required mode.
//
// The split is between what a shared account or a container has to traverse
// and what only root may read:
//
//	0755  the tree itself and everything the panel reads and renders into
//	0700  data/ and ssl/ (database, secret key, private key), and uploads/ and
//	      trash/ (operator content in transit)
type dirMode struct {
	mode os.FileMode
	why  string
}

// Layout is where install puts things. Root is a field rather than a constant so
// a test installs into a temporary directory; every path below is derived from
// it, so nothing in this package hard-codes /www.
//
// LinkPath is a field for the same reason: it is the one path outside Root, and
// a test that wrote /usr/local/bin/pbp would pollute the machine running it.
type Layout struct {
	Root     string
	UnitPath string
	LinkPath string
}

// DefaultLayout is the production layout from plan.md §2.
func DefaultLayout() Layout {
	return Layout{Root: DefaultRoot, UnitPath: DefaultUnitPath, LinkPath: DefaultLinkPath}
}

// PanelDir is the panel's own tree.
func (l Layout) PanelDir() string { return filepath.Join(l.Root, "panel") }

// BinDir holds the panel binary and supervisord.
func (l Layout) BinDir() string { return filepath.Join(l.PanelDir(), "bin") }

// BinaryPath is the installed panel binary: what the unit runs and what upgrade
// replaces.
func (l Layout) BinaryPath() string { return filepath.Join(l.BinDir(), BinaryName) }

// DataDir holds the database and the secret key.
func (l Layout) DataDir() string { return filepath.Join(l.PanelDir(), "data") }

// DBPath is the SQLite database (decision 6).
func (l Layout) DBPath() string { return filepath.Join(l.DataDir(), "panel.db") }

// SecretKeyPath is the panel's random key file.
func (l Layout) SecretKeyPath() string { return filepath.Join(l.DataDir(), "secret.key") }

// SSLDir holds the panel certificate and, later, site certificates and the ACME
// account key.
func (l Layout) SSLDir() string { return filepath.Join(l.PanelDir(), "ssl") }

// CertPath and KeyPath are the panel's own certificate pair.
func (l Layout) CertPath() string { return filepath.Join(l.SSLDir(), "panel.crt") }
func (l Layout) KeyPath() string  { return filepath.Join(l.SSLDir(), "panel.key") }

// LogsDir holds the panel, audit, site, app and cron logs.
func (l Layout) LogsDir() string { return filepath.Join(l.PanelDir(), "logs") }

// dirs is the tree install creates, in parent-before-child order, so a single
// pass both creates a fresh root and repairs an existing one.
//
// Directories whose contents are milestone-specific (nginx/sites,
// nginx/includes, php/<version>, supervisor/conf.d) are created here only up to
// the level install can decide; the milestone that owns their contents creates
// the rest. Ownership is not set here either: the www account's access to logs,
// sockets and site roots is granted by the service that needs it, so install
// does not guess at a policy the nginx and PHP milestones have not set yet.
func (l Layout) dirs() []struct {
	path string
	dirMode
} {
	const (
		tree   = 0o755
		secret = 0o700
	)
	return []struct {
		path string
		dirMode
	}{
		{l.Root, dirMode{tree, "shared root: sites and apps live under it"}},
		{l.PanelDir(), dirMode{tree, "panel internals"}},
		{l.BinDir(), dirMode{tree, "the binary must be executable by the unit"}},
		{l.DataDir(), dirMode{secret, "panel.db and secret.key"}},
		{l.SSLDir(), dirMode{secret, "panel.key and the ACME account key"}},
		// Only these two are mounted into the nginx container, which reaches
		// them as root, so 0755 here does not open the 0700 directory above.
		{filepath.Join(l.SSLDir(), "sites"), dirMode{tree, "one directory of certificate files per site; mounted read-only into nginx"}},
		{filepath.Join(l.SSLDir(), "acme-webroot"), dirMode{tree, "HTTP-01 challenge tokens; mounted read-only into nginx"}},
		{l.LogsDir(), dirMode{tree, "read by the log viewer, written by services"}},
		{filepath.Join(l.LogsDir(), "sites"), dirMode{tree, "one directory per site"}},
		{filepath.Join(l.LogsDir(), "apps"), dirMode{tree, "one directory per app"}},
		{filepath.Join(l.LogsDir(), "cron"), dirMode{tree, "cron job output"}},
		// nginx -t needs these before nginx has ever started: its temp directories
		// live under the cache, and the first reconcile validates the config.
		{filepath.Join(l.PanelDir(), "cache"), dirMode{tree, "caches the panel keeps"}},
		{filepath.Join(l.PanelDir(), "cache", "nginx"), dirMode{tree, "nginx temp files (client_temp and the rest)"}},
		{filepath.Join(l.PanelDir(), "nginx"), dirMode{tree, "generated nginx config"}},
		{filepath.Join(l.PanelDir(), "nginx", "sites"), dirMode{tree, "one file per site"}},
		{filepath.Join(l.PanelDir(), "nginx", "includes"), dirMode{tree, "one directory per site"}},
		{filepath.Join(l.PanelDir(), "php"), dirMode{tree, "one directory per PHP version"}},
		{filepath.Join(l.PanelDir(), "run"), dirMode{tree, "PHP sockets, shared with the nginx container"}},
		{filepath.Join(l.PanelDir(), "supervisor"), dirMode{tree, "supervisord configuration"}},
		{filepath.Join(l.PanelDir(), "supervisor", "conf.d"), dirMode{tree, "one file per app"}},
		{filepath.Join(l.PanelDir(), "nvm"), dirMode{tree, "Node versions installed from the Runtimes page"}},
		{filepath.Join(l.PanelDir(), "history"), dirMode{tree, "the last 10 versions of every edited config"}},
		{filepath.Join(l.PanelDir(), "uploads"), dirMode{secret, "in-progress chunked uploads"}},
		{filepath.Join(l.PanelDir(), "trash"), dirMode{secret, "deleted files awaiting a purge"}},
		{filepath.Join(l.Root, "services"), dirMode{tree, "site and app content"}},
		{filepath.Join(l.Root, "services", "sites"), dirMode{tree, "one directory per site; the folder is the web root, and apps default to theirs here"}},
		{filepath.Join(l.Root, "services", "apps"), dirMode{tree, "kept for hosts made before apps defaulted to services/sites"}},
	}
}
