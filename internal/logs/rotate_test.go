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
	for _, c := range []RotateConfig{{}, {Root: "/x"}, {Root: "/x", MaxBytes: 1}, {Root: "/x", MaxBytes: 1, Keep: 0}, {Root: "/x", MaxBytes: -1, Keep: 1}} {
		if _, err := NewRotator(c); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}
