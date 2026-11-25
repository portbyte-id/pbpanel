package clamav

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeRunner answers commands by program name and records every one it ran.
type fakeRunner struct {
	calls  []string
	answer func(c Command) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, c Command) ([]byte, error) {
	f.calls = append(f.calls, c.Name+" "+strings.Join(c.Args, " "))
	return f.answer(c)
}

func (f *fakeRunner) ran(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

type fakeInfo struct {
	os.FileInfo
	mode fs.FileMode
}

func (i fakeInfo) Mode() fs.FileMode { return i.mode }

// statFor reports the listed paths as existing, a socket for the daemon's.
func statFor(paths ...string) func(string) (os.FileInfo, error) {
	return func(name string) (os.FileInfo, error) {
		if slices.Contains(paths, name) {
			mode := fs.FileMode(0o755)
			if name == DaemonSocket {
				mode = os.ModeSocket
			}
			return fakeInfo{mode: mode}, nil
		}
		return nil, fs.ErrNotExist
	}
}

// healthy answers like a host with ClamAV running and signatures loaded.
func healthy(c Command) ([]byte, error) {
	switch {
	case c.Name == ScanBinary && len(c.Args) > 0 && c.Args[0] == "--version":
		return []byte("ClamAV 1.4.3/27800/Tue Nov 25 08:00:00 2026\n"), nil
	case c.Name == "systemctl" && c.Args[0] == "show":
		return []byte("LoadState=loaded\nActiveState=active\n"), nil
	}
	return nil, nil
}

// exitStatus is an *exec.ExitError with the given code, the error a scanner's
// own non-zero exit produces.
func exitStatus(t *testing.T, code string) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestExitHelper$")
	cmd.Env = append(os.Environ(), exitHelperEnv+"="+code)
	err := cmd.Run()
	if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		t.Skip("cannot make an exit error on this host")
	}
	return err
}

// exitHelperEnv names the variable that turns this test binary into a child that
// only exits with the code it is given. It is the stdlib's helper-process pattern:
// no shell, and the same binary on every host.
const exitHelperEnv = "CLAMAV_TEST_EXIT_CODE"

// TestExitHelper is not a test on its own: it runs only in the child started by
// exitStatus, and exits with the requested code.
func TestExitHelper(t *testing.T) {
	code := os.Getenv(exitHelperEnv)
	if code == "" {
		return
	}
	n, err := strconv.Atoi(code)
	if err != nil {
		t.Fatalf("bad exit code %q", code)
	}
	os.Exit(n)
}

func TestParseVersion(t *testing.T) {
	for _, tc := range []struct{ in, v, sig, date string }{
		{"ClamAV 1.4.3/27800/Tue Nov 25 08:00:00 2026\n", "1.4.3", "27800", "Tue Nov 25 08:00:00 2026"},
		{"ClamAV 1.4.3\n", "1.4.3", "", ""},
		{"ClamAV 1.4.3/0/Thu Jan  1 00:00:00 1970\n", "1.4.3", "", ""},
		{"command not found", "", "", ""},
	} {
		v, sig, date := ParseVersion(tc.in)
		if v != tc.v || sig != tc.sig || date != tc.date {
			t.Errorf("ParseVersion(%q) = %q %q %q, want %q %q %q", tc.in, v, sig, date, tc.v, tc.sig, tc.date)
		}
	}
}

func TestParseScan(t *testing.T) {
	out := `/srv/www/a.php: Php.Webshell-1 FOUND
/srv/www/odd: name: x/eicar.com: Eicar-Signature FOUND
/srv/www/locked: Permission denied. ERROR
ERROR: Can't open file

----------- SCAN SUMMARY -----------
Scanned files: 42
Infected files: 2
`
	scanned, infected, problems := ParseScan(out)
	if scanned != 42 {
		t.Errorf("scanned = %d, want 42", scanned)
	}
	if len(infected) != 2 || infected[0].Path != "/srv/www/a.php" || infected[0].Signature != "Php.Webshell-1" {
		t.Fatalf("infected = %+v", infected)
	}
	if infected[1].Path != "/srv/www/odd: name: x/eicar.com" || infected[1].Signature != "Eicar-Signature" {
		t.Errorf("a path holding \": \" must split on the last one, got %+v", infected[1])
	}
	if len(problems) != 2 {
		t.Errorf("problems = %v, want 2", problems)
	}
}

func TestStatus(t *testing.T) {
	t.Run("not installed", func(t *testing.T) {
		m := New(Options{Run: &fakeRunner{answer: healthy}, Stat: statFor()})
		st := m.Status(context.Background())
		if st.Installed || st.Ready() || st.Reason == "" {
			t.Errorf("status = %+v", st)
		}
	})
	t.Run("healthy", func(t *testing.T) {
		m := New(Options{Run: &fakeRunner{answer: healthy}, Stat: statFor(ScanBinary)})
		st := m.Status(context.Background())
		if !st.Installed || !st.Ready() || !st.DaemonActive || !st.UpdaterActive || st.Reason != "" {
			t.Errorf("status = %+v", st)
		}
		if st.Version != "1.4.3" || st.SignatureVersion != "27800" {
			t.Errorf("version = %q/%q", st.Version, st.SignatureVersion)
		}
	})
	t.Run("no signatures", func(t *testing.T) {
		r := &fakeRunner{answer: func(c Command) ([]byte, error) {
			if c.Name == ScanBinary {
				return []byte("ClamAV 1.4.3\n"), nil
			}
			return healthy(c)
		}}
		st := New(Options{Run: r, Stat: statFor(ScanBinary)}).Status(context.Background())
		if st.Ready() || !strings.Contains(st.Reason, "signatures") {
			t.Errorf("status = %+v", st)
		}
	})
	t.Run("daemon down", func(t *testing.T) {
		r := &fakeRunner{answer: func(c Command) ([]byte, error) {
			if c.Name == "systemctl" && strings.Contains(strings.Join(c.Args, " "), DaemonUnit) {
				return []byte("LoadState=loaded\nActiveState=failed\n"), nil
			}
			return healthy(c)
		}}
		st := New(Options{Run: r, Stat: statFor(ScanBinary)}).Status(context.Background())
		if st.DaemonActive || !st.Ready() || !strings.Contains(st.Reason, DaemonUnit) {
			t.Errorf("status = %+v", st)
		}
	})
}

func TestInstallOrder(t *testing.T) {
	r := &fakeRunner{answer: func(c Command) ([]byte, error) {
		if c.Name == FreshclamBin {
			return []byte("daily.cvd updated\n"), nil
		}
		return healthy(c)
	}}
	var installed []string
	var lines []string
	m := New(Options{
		Run:        r,
		Stat:       statFor(ScanBinary, DaemonSocket),
		SocketWait: time.Second, SocketPoll: time.Millisecond,
		Install: func(_ context.Context, pkgs []string) error { installed = pkgs; return nil },
	})
	if err := m.Install(context.Background(), func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(installed, ",") != "clamav,clamav-daemon,clamdscan,clamav-freshclam" {
		t.Errorf("packages = %v", installed)
	}
	// The updater is stopped before freshclam runs and the daemon restarted after,
	// so the first download holds no lock and clamd loads real signatures.
	idx := func(prefix string) int {
		for i, c := range r.calls {
			if strings.HasPrefix(c, prefix) {
				return i
			}
		}
		return -1
	}
	stop, fresh, restart := idx("systemctl stop "+FreshclamUnit), idx(FreshclamBin), idx("systemctl restart "+DaemonUnit)
	if stop < 0 || fresh < 0 || restart < 0 || stop >= fresh || fresh >= restart {
		t.Errorf("order wrong: stop=%d freshclam=%d restart=%d in %v", stop, fresh, restart, r.calls)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "daily.cvd updated") {
		t.Errorf("freshclam output missing from progress: %v", lines)
	}
}

func TestInstallFailsWhenSignaturesCannotBeFetched(t *testing.T) {
	r := &fakeRunner{answer: func(c Command) ([]byte, error) {
		if c.Name == FreshclamBin {
			return []byte("ERROR: Can't download\n"), errors.New("exit status 2")
		}
		return healthy(c)
	}}
	m := New(Options{Run: r, Stat: statFor(ScanBinary), Install: func(context.Context, []string) error { return nil }})
	err := m.Install(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "signatures") {
		t.Fatalf("err = %v", err)
	}
	if r.ran("systemctl restart") {
		t.Error("the daemon must not start without signatures")
	}
}

func TestInstallFailsWhenTheDaemonNeverListens(t *testing.T) {
	m := New(Options{
		Run: &fakeRunner{answer: healthy}, Stat: statFor(ScanBinary),
		SocketWait: 20 * time.Millisecond, SocketPoll: 5 * time.Millisecond,
		Install: func(context.Context, []string) error { return nil },
	})
	if err := m.Install(context.Background(), nil); err == nil || !strings.Contains(err.Error(), DaemonSocket) {
		t.Fatalf("err = %v", err)
	}
}

func TestScan(t *testing.T) {
	ctx := context.Background()

	t.Run("refuses when not installed", func(t *testing.T) {
		m := New(Options{Run: &fakeRunner{answer: healthy}, Stat: statFor()})
		if _, err := m.Scan(ctx, "/srv/www", nil); !errors.Is(err, ErrNotInstalled) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("refuses a relative path", func(t *testing.T) {
		m := New(Options{Run: &fakeRunner{answer: healthy}, Stat: statFor(ScanBinary)})
		if _, err := m.Scan(ctx, "www", nil); err == nil {
			t.Error("a relative path must be refused")
		}
	})
	t.Run("clean scan uses the daemon", func(t *testing.T) {
		r := &fakeRunner{answer: healthy}
		m := New(Options{Run: r, Stat: statFor(ScanBinary), CountFiles: func(string) int { return 42 }})
		res, err := m.Scan(ctx, "/srv/www", nil)
		if err != nil || res.Engine != "clamd" || len(res.Infected) != 0 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		// clamdscan reports no count, so the daemon's comes from the tree.
		if res.Scanned != 42 {
			t.Errorf("scanned = %d, want 42", res.Scanned)
		}
		if !r.ran(DaemonScanBin + " --fdpass --multiscan /srv/www") {
			t.Errorf("calls = %v", r.calls)
		}
	})
	t.Run("falls back to clamscan without the daemon", func(t *testing.T) {
		r := &fakeRunner{answer: func(c Command) ([]byte, error) {
			if c.Name == "systemctl" {
				return []byte("LoadState=loaded\nActiveState=inactive\n"), nil
			}
			return healthy(c)
		}}
		res, err := New(Options{Run: r, Stat: statFor(ScanBinary)}).Scan(ctx, "/srv/www", nil)
		if err != nil || res.Engine != "clamscan" || !r.ran(ScanBinary+" --recursive --infected /srv/www") {
			t.Fatalf("res=%+v err=%v calls=%v", res, err, r.calls)
		}
	})
	t.Run("infected files fail the scan and are reported", func(t *testing.T) {
		r := &fakeRunner{answer: func(c Command) ([]byte, error) {
			if c.Name == DaemonScanBin {
				return []byte("/srv/www/x.php: Php.Webshell-1 FOUND\n"), exitStatus(t, "1")
			}
			return healthy(c)
		}}
		var lines []string
		res, err := New(Options{Run: r, Stat: statFor(ScanBinary)}).Scan(ctx, "/srv/www", func(l string) { lines = append(lines, l) })
		if !errors.Is(err, ErrInfected) || len(res.Infected) != 1 || res.Infected[0].Signature != "Php.Webshell-1" {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if !strings.Contains(strings.Join(lines, "\n"), "/srv/www/x.php: Php.Webshell-1") {
			t.Errorf("the finding must be in the job output: %v", lines)
		}
	})
	t.Run("a scanner failure is not a clean result", func(t *testing.T) {
		r := &fakeRunner{answer: func(c Command) ([]byte, error) {
			if c.Name == DaemonScanBin {
				return []byte("ERROR: Could not connect to clamd\n"), exitStatus(t, "2")
			}
			return healthy(c)
		}}
		_, err := New(Options{Run: r, Stat: statFor(ScanBinary)}).Scan(ctx, "/srv/www", nil)
		if err == nil || errors.Is(err, ErrInfected) {
			t.Errorf("err = %v, want a failure that is not ErrInfected", err)
		}
	})
	t.Run("scans do not overlap", func(t *testing.T) {
		release := make(chan struct{})
		started := make(chan struct{})
		r := &fakeRunner{answer: func(c Command) ([]byte, error) {
			if c.Name == DaemonScanBin {
				close(started)
				<-release
			}
			return healthy(c)
		}}
		m := New(Options{Run: r, Stat: statFor(ScanBinary)})
		done := make(chan error, 1)
		go func() { _, err := m.Scan(ctx, "/a", nil); done <- err }()
		<-started
		if !m.Status(ctx).Scanning {
			t.Error("Status must report the running scan")
		}
		if _, err := m.Scan(ctx, "/b", nil); !errors.Is(err, ErrScanBusy) {
			t.Errorf("second scan err = %v, want ErrScanBusy", err)
		}
		close(release)
		if err := <-done; err != nil {
			t.Errorf("first scan err = %v", err)
		}
	})
}

func TestDefaultCountFiles(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a", "sub/b", "sub/deep/c"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if n := defaultCountFiles(dir); n != 3 {
		t.Errorf("count = %d, want 3 regular files (directories do not count)", n)
	}
	if n := defaultCountFiles(filepath.Join(dir, "missing")); n != 0 {
		t.Errorf("count of a missing path = %d, want 0", n)
	}
}

func TestUninstall(t *testing.T) {
	ctx := context.Background()
	if err := New(Options{Run: &fakeRunner{answer: healthy}, Stat: statFor()}).Uninstall(ctx, nil); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("err = %v", err)
	}
	gone := false
	r := &fakeRunner{answer: func(c Command) ([]byte, error) {
		if len(c.Args) > 0 && c.Args[0] == "purge" {
			gone = true
		}
		return healthy(c)
	}}
	stat := func(name string) (os.FileInfo, error) {
		if name == ScanBinary && !gone {
			return fakeInfo{mode: 0o755}, nil
		}
		return nil, fs.ErrNotExist
	}
	var removed []string
	m := New(Options{Run: r, Stat: stat, RemoveAll: func(p string) error { removed = append(removed, p); return nil }})
	if err := m.Uninstall(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// The purge leaves the downloaded .sign files, so the directory goes too.
	if len(removed) != 1 || removed[0] != DatabaseDir {
		t.Errorf("removed = %v, want [%s]", removed, DatabaseDir)
	}
	if !slices.ContainsFunc(r.calls, func(c string) bool {
		return strings.HasSuffix(c, " purge -y --autoremove clamav clamav-daemon clamdscan clamav-freshclam")
	}) {
		t.Errorf("calls = %v", r.calls)
	}
}
