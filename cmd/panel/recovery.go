package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/auth"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/version"
)

const (
	// defaultDataDir is where install puts the database (decision 6).
	defaultDataDir = "/www/panel/data"

	// dataDirEnv points the recovery commands at another data directory. It is
	// how the tests and a smoke run reach a database that is not at the default
	// location.
	dataDirEnv = "PB_PANEL_DATA_DIR"

	// defaultAdminUser is the account `panel reset-password` creates when install
	// has not made one yet.
	defaultAdminUser = "admin"
)

// databasePath is the database the recovery commands read and write.
func databasePath() string {
	if dir := os.Getenv(dataDirEnv); dir != "" {
		return filepath.Join(dir, "panel.db")
	}
	return filepath.Join(defaultDataDir, "panel.db")
}

// openStore opens the database for a one-shot command. A path that does not
// exist is an error rather than something to create: `panel set` before
// `pbp install` must not leave an empty database behind for install to trip
// over later.
func openStore(path string) (*store.Store, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no database at %s: run `pbp install` first", path)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return store.Open(path)
}

// infoReport is what `panel info` shows. It is a struct so the fields can be
// asserted without parsing the printed text.
type infoReport struct {
	Version  string
	Database string
	Bind     string
	Port     int
	URL      string
}

// readInfo reads the report from the database. It never talks to the daemon, so
// it answers when the panel is stopped, wedged, or bound to a port the operator
// has lost track of.
func readInfo(ctx context.Context, path string) (infoReport, error) {
	st, err := openStore(path)
	if err != nil {
		return infoReport{}, err
	}
	defer func() { _ = st.Close() }()

	reg, err := settings.New(st)
	if err != nil {
		return infoReport{}, err
	}
	port, err := reg.Int(ctx, "panel.port")
	if err != nil {
		return infoReport{}, err
	}
	bind, err := reg.Str(ctx, "panel.bind")
	if err != nil {
		return infoReport{}, err
	}

	return infoReport{
		Version:  version.Version,
		Database: path,
		Bind:     bind,
		Port:     port,
		URL:      panelURL(bind, port),
	}, nil
}

func printInfo(w io.Writer, r infoReport) {
	printf(w, "version   %s\n", r.Version)
	printf(w, "database  %s\n", r.Database)
	printf(w, "bind      %s\n", r.Bind)
	printf(w, "port      %d\n", r.Port)
	printf(w, "url       %s\n", r.URL)
}

// panelURL is the address the panel answers on. A wildcard bind is printed as
// stored: the panel is reachable on every interface, and inventing a hostname
// here would print a URL that may not resolve.
func panelURL(bind string, port int) string {
	return "https://" + net.JoinHostPort(bind, strconv.Itoa(port))
}

// info implements `panel info`.
func info(ctx context.Context, w io.Writer, path string) error {
	r, err := readInfo(ctx, path)
	if err != nil {
		return err
	}
	printInfo(w, r)
	return nil
}

// newAuthService builds the auth service the recovery commands use. Only
// SetPassword and Username are called, and both work straight off the store. The
// three policy functions belong to login, which the CLI never performs, so they
// return zero: a login attempted through this service fails closed (a session
// created here would expire at once) instead of running with limits nobody
// configured.
func newAuthService(st *store.Store) *auth.Service {
	return auth.NewService(st, auth.SystemClock{},
		func() time.Duration { return 0 },
		func() int { return 0 },
		func() time.Duration { return 0 },
	)
}

// resetPassword implements `panel reset-password`: a generated password printed
// once, its hash stored, and every live session revoked in the same transaction
// (auth.SetPassword does both halves together, decision 5).
func resetPassword(ctx context.Context, w io.Writer, path string) error {
	st, err := openStore(path)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	svc := newAuthService(st)

	username, err := svc.Username(ctx)
	if err != nil {
		return err
	}
	if username == "" {
		username = defaultAdminUser
	}

	password, err := generatePassword()
	if err != nil {
		return err
	}
	if err := svc.SetPassword(ctx, username, password); err != nil {
		return err
	}

	printf(w, "New password for %s:\n\n    %s\n\n", username, password)
	printf(w, "It is shown once and is stored only as an argon2id hash.\n")
	printf(w, "Every existing session has been signed out.\n")
	return nil
}

// unlockLogin implements `panel unlock-login`: it clears every failed-login
// lockout. The running daemon applies it at its next login attempt, so no restart
// is needed.
func unlockLogin(ctx context.Context, w io.Writer, path string) error {
	st, err := openStore(path)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if err := newAuthService(st).UnlockLogin(ctx); err != nil {
		return err
	}
	printf(w, "Failed-login lockouts are cleared; the panel applies this at the next login attempt.\n")
	return nil
}

// passwordAlphabet leaves out the characters an operator reading a console
// confuses: 0 and O, 1 and I, l.
const passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// generatedPasswordLen characters from a 57-symbol alphabet is about 116 bits,
// well past the point where the password itself is the weak link.
const generatedPasswordLen = 20

func generatePassword() (string, error) {
	limit := big.NewInt(int64(len(passwordAlphabet)))
	out := make([]byte, generatedPasswordLen)
	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		out[i] = passwordAlphabet[n.Int64()]
	}
	return string(out), nil
}

// setSetting implements `panel set`: the same registry and the same validators
// the Settings page writes through, so a value that survives one path survives
// the other.
func setSetting(ctx context.Context, w io.Writer, path, name, value string) error {
	st, err := openStore(path)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	reg, err := settings.New(st)
	if err != nil {
		return err
	}

	if err := reg.Set(ctx, name, value); err != nil {
		// The value was rejected, so nothing was written; the validator's own
		// message is what the operator needs to see.
		return fmt.Errorf("set %s: %w", name, err)
	}

	s, err := reg.Get(ctx, name)
	if err != nil {
		return err
	}

	printf(w, "%s = %s\n", s.Name, s.Value)
	if s.RestartRequired {
		printf(w, "%s is marked restart-required: a running panel may not apply it until it is restarted.\n", s.Name)
		printf(w, "Restart it with: systemctl restart panel\n")
	}
	return nil
}

// setHelp describes the keys `panel set` accepts. The restart marker comes from
// each key's own RestartRequired flag, so a key added to the registry shows up
// here without a second list to keep in step.
func setHelp() string {
	var b strings.Builder
	b.WriteString("Writes one value to the panel database through the same validators the Settings\n")
	b.WriteString("page uses. A rejected value changes nothing and exits non-zero.\n\n")

	groups, err := settings.Catalog()
	if err != nil {
		// Only an invalid key table reaches here, and the settings tests pin that
		// table; `set` itself reports the same fault when it runs.
		fmt.Fprintf(&b, "the settings registry is invalid: %v\n", err)
		return b.String()
	}

	b.WriteString("A key marked \"restart-required\" may need `systemctl restart panel` before a\n")
	b.WriteString("running panel applies it. The others take effect while the panel runs.\n\n")
	for _, g := range groups {
		fmt.Fprintf(&b, "%s:\n", g.Name)
		for _, m := range g.Keys {
			mark := ""
			if m.RestartRequired {
				mark = "restart-required"
			}
			fmt.Fprintf(&b, "  %-22s %-9s default %-12s %s\n", m.Name, m.Type, m.Default, mark)
			fmt.Fprintf(&b, "      %s\n", m.Description)
		}
	}
	return b.String()
}
