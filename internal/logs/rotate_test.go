package logs

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func jsonMarshal(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func readAny(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = zr.Close() }()
		r = zr
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRotateKeepsExactlyNAndCallsHookOnce(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "nginx", "access.log")
	small := filepath.Join(root, "small.log")
	writeFile(t, small, "tiny\n")

	var hookCalls int
	rot, err := NewRotator(RotateConfig{Root: root, MaxBytes: 10, Keep: 3, Compress: true},
		func(context.Context) error {
			hookCalls++
			// The live file is gone and the .1 file is still plain while writers reopen.
			if _, err := os.Stat(live); !os.IsNotExist(err) {
				t.Errorf("live file still present at hook time: %v", err)
			}
			if _, err := os.Stat(live + ".1"); err != nil {
				t.Errorf("rotated file missing at hook time: %v", err)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}

	var all []string
	for gen := 1; gen <= 6; gen++ {
		line := fmt.Sprintf("generation-%d-line\n", gen)
		writeFile(t, live, line)
		all = append(all, line)
		rep, err := rot.Rotate(context.Background())
		if err != nil {
			t.Fatalf("gen %d: %v", gen, err)
		}
		if len(rep.Rotated) != 1 || rep.Rotated[0] != live {
			t.Fatalf("gen %d rotated %v", gen, rep.Rotated)
		}
		if hookCalls != gen {
			t.Fatalf("hook calls %d after %d passes", hookCalls, gen)
		}
	}
	// Exactly Keep rotated files, all gzipped, plus nothing else.
	got := names(t, filepath.Join(root, "nginx"))
	want := []string{"access.log.1.gz", "access.log.2.gz", "access.log.3.gz"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", got, want)
	}
	// Newest rotation is generation 6; the newest three survive.
	for i, wantLine := range []string{all[5], all[4], all[3]} {
		p := fmt.Sprintf("%s.%d.gz", live, i+1)
		if g := readAny(t, p); g != wantLine {
			t.Errorf("%s = %q, want %q", p, g, wantLine)
		}
	}
	// A file below the threshold is untouched and does not call the hook.
	if readAny(t, small) != "tiny\n" {
		t.Error("small file changed")
	}
}

func TestRotateNoLossWhenNothingExpires(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "app.log")
	rot, _ := NewRotator(RotateConfig{Root: root, MaxBytes: 1, Keep: 10, Compress: false})
	var want []string
	for i := 0; i < 5; i++ {
		line := fmt.Sprintf("row-%d\n", i)
		writeFile(t, live, line)
		want = append(want, line)
		if _, err := rot.Rotate(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for i := 1; i <= 5; i++ {
		got = append(got, readAny(t, fmt.Sprintf("%s.%d", live, i)))
	}
	// .1 is the newest.
	for i, g := range got {
		if g != want[len(want)-1-i] {
			t.Errorf(".%d = %q", i+1, g)
		}
	}
}

func TestRotateToleratesMissingFilesAndRoot(t *testing.T) {
	rot, _ := NewRotator(RotateConfig{Root: filepath.Join(t.TempDir(), "absent"), MaxBytes: 1, Keep: 3})
	if rep, err := rot.Rotate(context.Background()); err != nil || len(rep.Rotated) != 0 {
		t.Fatalf("missing root: %+v %v", rep, err)
	}

	// Gaps in the numbered sequence: only .2 exists.
	root := t.TempDir()
	live := filepath.Join(root, "x.log")
	writeFile(t, live, "live\n")
	writeFile(t, live+".2", "two\n")
	hooks := 0
	rot, _ = NewRotator(RotateConfig{Root: root, MaxBytes: 1, Keep: 3}, func(context.Context) error { hooks++; return nil })
	if _, err := rot.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(t, root), ","); got != "x.log.1,x.log.3" {
		t.Fatalf("files = %s", got)
	}
	// Nothing to rotate: no hook.
	if _, err := rot.Rotate(context.Background()); err != nil || hooks != 1 {
		t.Fatalf("second pass hooks=%d err=%v", hooks, err)
	}
}

func TestRotateHookFailureSkipsCompressingNewestAndReportsError(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "php.log")
	writeFile(t, live, "payload\n")
	boom := errors.New("fpm signal failed")
	ok := 0
	rot, _ := NewRotator(RotateConfig{Root: root, MaxBytes: 1, Keep: 2, Compress: true},
		func(context.Context) error { return boom },
		func(context.Context) error { ok++; return nil })
	_, err := rot.Rotate(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if ok != 1 {
		t.Error("later hook was not run after an earlier failure")
	}
	if got := strings.Join(names(t, root), ","); got != "php.log.1" {
		t.Fatalf("files = %s (newest rotation must stay plain)", got)
	}
}

func TestRotateIgnoresSymlinksAndOutsideFiles(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "victim.log")
	writeFile(t, target, "do not touch\n")
	root := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "link.log")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	rot, _ := NewRotator(RotateConfig{Root: root, MaxBytes: 1, Keep: 2})
	rep, err := rot.Rotate(context.Background())
	if err != nil || len(rep.Rotated) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if readAny(t, target) != "do not touch\n" {
		t.Error("symlink target modified")
	}
}

func TestRotatedSetIsSearchable(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "site.log")
	rot, _ := NewRotator(RotateConfig{Root: root, MaxBytes: 1, Keep: 3, Compress: true})
	writeFile(t, live, "alpha NEEDLE\n")
	if _, err := rot.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeFile(t, live, "beta\nbeta NEEDLE\n")
	r := newReg(t, live)
	got, _ := collectSearch(t, r, Query{Pattern: "NEEDLE"}, SearchOptions{})
	if len(got) != 2 || got[0].File != "site.log.1.gz" || got[1].File != "site.log" {
		t.Fatalf("got %+v", got)
	}
}

func TestNewRotatorValidation(t *testing.T) {
	for _, c := range []RotateConfig{{}, {Root: "/x"}, {Root: "/x", MaxBytes: 1}, {Root: "/x", MaxBytes: 1, Keep: 0}, {Root: "/x", MaxBytes: -1, Keep: 1},
		{Root: "/x", MaxBytes: 1, Keep: 1, MaxAge: -time.Hour}, {Root: "/x", MaxBytes: 1, Keep: 1, RetainAge: -time.Minute}} {
		if _, err := NewRotator(c); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	if _, err := NewRotator(RotateConfig{Root: "/x", MaxBytes: 1, Keep: 1, MaxAge: time.Hour, RetainAge: 24 * time.Hour}); err != nil {
		t.Errorf("refused a valid age policy: %v", err)
	}
}

// age sets a file's mtime.
func age(t *testing.T, path string, d time.Duration) {
	t.Helper()
	when := time.Now().Add(-d)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func mtime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime()
}

// A log that stays small but went quiet is still cut on a schedule: the age
// trigger is the mtime of the last write.
func TestRotateOnAge(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, "quiet.log")
	fresh := filepath.Join(root, "busy.log")
	writeFile(t, stale, "old\n")
	writeFile(t, fresh, "new\n")
	// Far below MaxBytes, so only the age trigger can fire.
	age(t, stale, 30*time.Hour)

	hooks := 0
	rot, err := NewRotator(RotateConfig{Root: root, MaxBytes: 1 << 20, Keep: 3, MaxAge: 24 * time.Hour, Compress: true},
		func(context.Context) error { hooks++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	rep, err := rot.Rotate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rotated) != 1 || rep.Rotated[0] != stale {
		t.Fatalf("rotated = %v, want only the stale log", rep.Rotated)
	}
	if hooks != 1 {
		t.Errorf("the reopen hook ran %d times, want 1", hooks)
	}
	if got := strings.Join(names(t, root), ","); got != "busy.log,quiet.log.1.gz" {
		t.Fatalf("files = %s", got)
	}
	// The hook contract is unchanged: it runs after the rename and before the
	// gzip, so a writer has reopened by the time the file is compressed.
	if readAny(t, stale+".1.gz") != "old\n" {
		t.Error("the rotated content was lost")
	}
	if readAny(t, fresh) != "new\n" {
		t.Error("the fresh log was touched")
	}

	// The next pass has nothing stale: the rotated file's mtime is the moment of
	// rotation, not that of its content.
	if rep, err = rot.Rotate(context.Background()); err != nil || len(rep.Rotated) != 0 || hooks != 1 {
		t.Fatalf("second pass: %+v hooks=%d err=%v", rep, hooks, err)
	}
}

// MaxAge zero is exactly today's behaviour: size only.
func TestRotateWithMaxAgeZeroIgnoresAge(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "ancient.log")
	writeFile(t, old, "x\n")
	age(t, old, 400*24*time.Hour)
	hooks := 0
	rot, _ := NewRotator(RotateConfig{Root: root, MaxBytes: 10, Keep: 2},
		func(context.Context) error { hooks++; return nil })
	rep, err := rot.Rotate(context.Background())
	if err != nil || len(rep.Rotated) != 0 || hooks != 0 {
		t.Fatalf("%+v hooks=%d err=%v; an old but small file must stay", rep, hooks, err)
	}
	if got := strings.Join(names(t, root), ","); got != "ancient.log" {
		t.Fatalf("files = %s", got)
	}
}

// The size rule is still the size rule when an age policy is also set.
func TestRotateOnSizeWithAnAgePolicySet(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "big.log")
	writeFile(t, live, strings.Repeat("x", 2048)+"\n")
	// Fresh, so only the size trigger can fire.
	rot, _ := NewRotator(RotateConfig{Root: root, MaxBytes: 1024, Keep: 1, MaxAge: 24 * time.Hour})
	rep, err := rot.Rotate(context.Background())
	if err != nil || len(rep.Rotated) != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
}

// Both triggers at once: two files, one over each threshold, both rotate in one
// pass and the hook runs once for the pass.
func TestRotateOnEitherTriggerInOnePass(t *testing.T) {
	root := t.TempDir()
	big := filepath.Join(root, "big.log")
	old := filepath.Join(root, "old.log")
	writeFile(t, big, strings.Repeat("x", 2048)+"\n")
	writeFile(t, old, "y\n")
	age(t, old, 48*time.Hour)
	hooks := 0
	rot, _ := NewRotator(RotateConfig{Root: root, MaxBytes: 1024, Keep: 1, MaxAge: 24 * time.Hour},
		func(context.Context) error { hooks++; return nil })
	rep, err := rot.Rotate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rotated) != 2 || hooks != 1 {
		t.Fatalf("%+v hooks=%d", rep, hooks)
	}
}

// RetainAge prunes a rotated file older than the retention and keeps a newer
// one, independently of the count.
func TestRotatePrunesByRetainAge(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "app.log")
	oldGz := filepath.Join(root, "app.log.2.gz")
	newPlain := filepath.Join(root, "app.log.3")
	writeGz(t, oldGz, "old\n")
	writeFile(t, newPlain, "new\n")
	age(t, oldGz, 10*24*time.Hour)
	age(t, newPlain, time.Hour)
	// A live log older than the retention must survive: only rotated files age
	// out, because only a rotated file's writer has reopened.
	writeFile(t, live, "live\n")
	age(t, live, 30*24*time.Hour)
	// A rotated-looking name that is not a slot number is an app's own file.
	timestamped := filepath.Join(root, "svc.log.20260101")
	writeFile(t, timestamped, "keep\n")
	age(t, timestamped, 60*24*time.Hour)

	rot, err := NewRotator(RotateConfig{Root: root, MaxBytes: 1 << 20, Keep: 10, RetainAge: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := rot.Rotate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Pruned) != 1 || rep.Pruned[0] != oldGz {
		t.Fatalf("pruned = %v", rep.Pruned)
	}
	if got := strings.Join(names(t, root), ","); got != "app.log,app.log.3,svc.log.20260101" {
		t.Fatalf("files = %s", got)
	}
}

// RetainAge zero is the count-only retention.
func TestRotateWithRetainAgeZeroKeepsOldRotations(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "app.log.1")
	writeFile(t, old, "old\n")
	age(t, old, 400*24*time.Hour)
	rot, _ := NewRotator(RotateConfig{Root: root, MaxBytes: 1 << 20, Keep: 5})
	if rep, err := rot.Rotate(context.Background()); err != nil || len(rep.Pruned) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if got := strings.Join(names(t, root), ","); got != "app.log.1" {
		t.Fatalf("files = %s", got)
	}
}

// A rotation stamps the newest rotated file with the moment of rotation, which
// is what RetainAge then counts from. Without it, a file written a week before
// it was rotated would be pruned on the very next pass.
func TestRotateStampsTheRotatedFile(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "app.log")
	writeFile(t, live, "payload\n")
	age(t, live, 10*24*time.Hour)
	rot, _ := NewRotator(RotateConfig{Root: root, MaxBytes: 1 << 20, Keep: 5, MaxAge: 24 * time.Hour, RetainAge: 7 * 24 * time.Hour})
	if _, err := rot.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(mtime(t, live+".1")); d > time.Minute {
		t.Fatalf("rotated file mtime is %v old, want the moment of rotation", d)
	}
	// The content's age is irrelevant now: the retention counts from here.
	rep, err := rot.Rotate(context.Background())
	if err != nil || len(rep.Pruned) != 0 {
		t.Fatalf("pruned a just-rotated file: %+v %v", rep, err)
	}
}

// The hook contract survives an age-triggered rotation: it runs once, after the
// rename and before compression, exactly as a size-triggered one does.
func TestAgeRotationHookContract(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "php8.4", "error.log")
	writeFile(t, live, "boom\n")
	age(t, live, 48*time.Hour)
	calls := 0
	rot, _ := NewRotator(RotateConfig{Root: root, MaxBytes: 1 << 20, Keep: 2, MaxAge: 24 * time.Hour, Compress: true},
		func(context.Context) error {
			calls++
			if _, err := os.Stat(live); !os.IsNotExist(err) {
				t.Errorf("live file still present at hook time: %v", err)
			}
			if _, err := os.Stat(live + ".1"); err != nil {
				t.Errorf("rotated file missing at hook time: %v", err)
			}
			return nil
		})
	rep, err := rot.Rotate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(rep.Rotated) != 1 || rep.Compressed != 1 {
		t.Fatalf("calls=%d report=%+v", calls, rep)
	}
	if got := strings.Join(names(t, filepath.Dir(live)), ","); got != "error.log.1.gz" {
		t.Fatalf("files = %s", got)
	}
}
