package logs

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeGz(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func newReg(t *testing.T, path string) *Registry {
	t.Helper()
	r := NewRegistry(nil)
	if err := r.Add(Source{ID: "app", Label: "App", Kind: KindApp, Path: path}); err != nil {
		t.Fatal(err)
	}
	return r
}

func collectSearch(t *testing.T, r *Registry, q Query, o SearchOptions) ([]Match, SearchStats) {
	t.Helper()
	var got []Match
	st, err := r.Search(context.Background(), "app", q, o, func(m Match) error {
		got = append(got, m)
		return nil
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return got, st
}

func TestSearchAcrossRotatedGzFixture(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "app.log")
	writeGz(t, base+".2.gz", "old ok\nold ERROR gz-one\nold ok\n")
	writeFile(t, base+".1", "mid ERROR plain\nmid ok\n")
	writeFile(t, base, "new ok\nnew ERROR live\nlast line without newline ERROR")
	writeFile(t, filepath.Join(dir, "other.log"), "ERROR must not appear\n")
	writeFile(t, base+".bak", "ERROR must not appear\n")
	r := newReg(t, base)

	got, st := collectSearch(t, r, Query{Pattern: "ERROR"}, SearchOptions{})
	want := []string{
		"app.log.2.gz:2:old ERROR gz-one",
		"app.log.1:1:mid ERROR plain",
		"app.log:2:new ERROR live",
		"app.log:3:last line without newline ERROR",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d matches %+v", len(got), got)
	}
	for i, m := range got {
		if s := fmt.Sprintf("%s:%d:%s", m.File, m.Line, m.Text); s != want[i] {
			t.Errorf("match %d = %q, want %q", i, s, want[i])
		}
	}
	if st.Files != 3 || st.WindowCapped || st.LimitHit {
		t.Errorf("stats = %+v", st)
	}

	// Regex, case-insensitive.
	got, _ = collectSearch(t, r, Query{Pattern: `^old .*GZ-ONE$`, Regex: true, CaseInsensitive: true}, SearchOptions{})
	if len(got) != 1 || got[0].File != "app.log.2.gz" {
		t.Fatalf("regex got %+v", got)
	}
	// Literal with regex metacharacters is not interpreted.
	got, _ = collectSearch(t, r, Query{Pattern: "o.d", CaseInsensitive: true}, SearchOptions{})
	if len(got) != 0 {
		t.Fatalf("literal dot matched %+v", got)
	}
	// Limit.
	got, st = collectSearch(t, r, Query{Pattern: "ERROR"}, SearchOptions{Limit: 2})
	if len(got) != 2 || !st.LimitHit || got[1].File != "app.log.1" {
		t.Fatalf("limit got %+v %+v", got, st)
	}
}

func TestSearchWindowSkipsOldestData(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "app.log")
	writeGz(t, base+".1.gz", "x-old-1\nx-old-2\nx-old-3\n")
	writeFile(t, base, "x-new-1\nx-new-2\n")
	r := newReg(t, base)

	// 16 bytes covers the live file exactly: nothing older is read.
	got, st := collectSearch(t, r, Query{Pattern: "x-"}, SearchOptions{lastBytes: 16})
	if len(got) != 2 || got[0].Text != "x-new-1" || !st.WindowCapped {
		t.Fatalf("got %+v %+v", got, st)
	}
	// 16+8: the straddling gz file is entered on a line boundary (24 - 8 = 16
	// skipped), so only x-old-3 is read, flagged partial.
	got, _ = collectSearch(t, r, Query{Pattern: "x-"}, SearchOptions{lastBytes: 24})
	if len(got) != 3 || got[0].Text != "x-old-3" || !got[0].Partial || got[0].Line != 1 {
		t.Fatalf("got %+v", got)
	}
	// Entering mid-line drops the fragment instead of returning half a line.
	got, _ = collectSearch(t, r, Query{Pattern: "x-"}, SearchOptions{lastBytes: 20})
	if len(got) != 2 || got[0].Text != "x-new-1" {
		t.Fatalf("mid-line got %+v", got)
	}
}

func TestSearchBadQueryAndUnavailable(t *testing.T) {
	dir := t.TempDir()
	r := newReg(t, filepath.Join(dir, "missing.log"))
	for _, q := range []Query{{}, {Pattern: "(", Regex: true}, {Pattern: strings.Repeat("a", maxPatternLen+1)}} {
		if _, err := r.Search(context.Background(), "app", q, SearchOptions{}, nil); !errors.Is(err, ErrBadQuery) {
			t.Errorf("query %q: err = %v", q.Pattern, err)
		}
	}
	_, err := r.Search(context.Background(), "app", Query{Pattern: "x"}, SearchOptions{}, nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("missing log: err = %v", err)
	}
}

func TestResolveRejectsPathsAndUnknownIDs(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.txt")
	writeFile(t, secret, "top secret\n")
	r := newReg(t, filepath.Join(dir, "app.log"))
	for _, id := range []string{secret, "../secret.txt", "..", "app/../app", "/etc/passwd", "", "nope", "app ", "app\x00"} {
		if _, err := r.Resolve(context.Background(), id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Resolve(%q) = %v, want ErrNotFound", id, err)
		}
		err := r.Tail(context.Background(), id, TailOptions{}, func(string) error { return nil })
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Tail(%q) = %v", id, err)
		}
		if _, err := r.Search(context.Background(), id, Query{Pattern: "x"}, SearchOptions{}, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("Search(%q) = %v", id, err)
		}
	}
}

func TestSourceValidationAndProviders(t *testing.T) {
	r := NewRegistry(nil)
	bad := []Source{
		{ID: "../x", Label: "l", Kind: KindApp, Path: "/a/b.log"},
		{ID: "x", Label: "l", Kind: KindApp, Path: "relative/b.log"},
		{ID: "x", Label: "l", Kind: KindDocker, Container: "--privileged"},
		{ID: "x", Label: "l", Kind: KindApp},
		{ID: "x", Label: "l", Kind: KindApp, Path: "/a", Container: "c"},
		{ID: "x", Kind: KindApp, Path: "/a/b.log"},
	}
	for _, s := range bad {
		if err := r.Add(s); !errors.Is(err, ErrInvalidSource) {
			t.Errorf("Add(%+v) = %v", s, err)
		}
	}
	good := Source{ID: "panel", Label: "Panel", Kind: KindPanel, Path: filepath.Join(t.TempDir(), "p.log")}
	if err := r.Add(good); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(good); !errors.Is(err, ErrInvalidSource) {
		t.Errorf("duplicate add = %v", err)
	}
	r.AddProvider(ProviderFunc(func(context.Context) ([]Source, error) {
		return []Source{
			{ID: "site-b", Label: "b.example", Kind: KindSite, Path: good.Path},
			{ID: "panel", Label: "shadow", Kind: KindSite, Path: good.Path}, // clash: static wins
			{ID: "bad id", Label: "x", Kind: KindSite, Path: good.Path},     // invalid: dropped
			{ID: "ctr", Label: "web", Kind: KindDocker, Container: "web_1"},
		}, nil
	}))
	r.AddProvider(ProviderFunc(func(context.Context) ([]Source, error) { return nil, errors.New("boom") }))
	list, err := r.List(context.Background())
	if err == nil {
		t.Error("provider error not reported")
	}
	var ids []string
	for _, s := range list {
		ids = append(ids, s.ID)
	}
	if got := strings.Join(ids, ","); got != "ctr,panel,site-b" {
		t.Errorf("ids = %s", got)
	}
	if s, err := r.Resolve(context.Background(), "panel"); err != nil || s.Label != "Panel" {
		t.Errorf("Resolve panel = %+v %v", s, err)
	}
}

func TestTailLastLinesNonFollow(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "app.log")
	var sb strings.Builder
	for i := 1; i <= 100000; i++ {
		fmt.Fprintf(&sb, "line %d\r\n", i)
	}
	sb.WriteString("tail")
	writeFile(t, base, sb.String())
	r := newReg(t, base)

	var got []string
	err := r.Tail(context.Background(), "app", TailOptions{Lines: 3}, func(l string) error {
		got = append(got, l)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "line 99999|line 100000|tail"; strings.Join(got, "|") != want {
		t.Fatalf("got %q want %q", got, want)
	}

	writeFile(t, base, "only\n")
	got = nil
	_ = r.Tail(context.Background(), "app", TailOptions{Lines: 50}, func(l string) error { got = append(got, l); return nil })
	if len(got) != 1 || got[0] != "only" {
		t.Fatalf("short file got %q", got)
	}
}

func TestTailLongLineIsTruncated(t *testing.T) {
	base := filepath.Join(t.TempDir(), "app.log")
	writeFile(t, base, strings.Repeat("a", maxLine*2)+"\nnext\n")
	r := newReg(t, base)
	var got []string
	_ = r.Tail(context.Background(), "app", TailOptions{}, func(l string) error { got = append(got, l); return nil })
	if len(got) != 2 || got[1] != "next" || !strings.HasSuffix(got[0], truncMarker) || len(got[0]) > maxLine+len(truncMarker) {
		t.Fatalf("unexpected: %d lines, first len %d", len(got), len(got[0]))
	}
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func recv(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	select {
	case got, ok := <-ch:
		if !ok || got != want {
			t.Fatalf("got %q (open=%v), want %q", got, ok, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %q", want)
	}
}

func TestTailFollowsAcrossRotationAndTruncation(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "app.log")
	writeFile(t, base, "a1\na2\npar")
	r := newReg(t, base)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lines, errc := r.TailChan(ctx, "app", TailOptions{Lines: 10, Follow: true, PollInterval: 10 * time.Millisecond})
	recv(t, lines, "a1")
	recv(t, lines, "a2")

	// The unterminated "par" is held back until its newline arrives.
	appendTo(t, base, "tial\na3\n")
	recv(t, lines, "partial")
	recv(t, lines, "a3")

	// Rotation: a line lands in the old file after the rename, then a new file
	// appears. The old file is drained first, then the new one read from byte 0.
	if err := os.Rename(base, base+".1"); err != nil {
		t.Fatal(err)
	}
	appendTo(t, base+".1", "a4-late-in-old\n")
	writeFile(t, base, "b1\nb2\n")
	recv(t, lines, "a4-late-in-old")
	recv(t, lines, "b1")
	recv(t, lines, "b2")

	// Truncation in place (copytruncate style).
	if err := os.Truncate(base, 0); err != nil {
		t.Fatal(err)
	}
	appendTo(t, base, "c1\n")
	recv(t, lines, "c1")

	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("tail end = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tail did not stop")
	}
	if _, ok := <-lines; ok {
		t.Fatal("lines not closed")
	}
}

func TestTailFollowSurvivesMissingFileDuringRotation(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "app.log")
	writeFile(t, base, "one\n")
	r := newReg(t, base)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lines, _ := r.TailChan(ctx, "app", TailOptions{Follow: true, PollInterval: 10 * time.Millisecond})
	recv(t, lines, "one")
	if err := os.Rename(base, base+".1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond) // live file is absent for a few polls
	writeFile(t, base, "two\n")
	recv(t, lines, "two")
}

type fakeDocker struct {
	body    string
	gotName atomic.Value
	opts    atomic.Value
}

func (f *fakeDocker) ContainerLogs(_ context.Context, name string, o DockerOptions) (io.ReadCloser, error) {
	f.gotName.Store(name)
	f.opts.Store(o)
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func TestDockerSourcesTailAndSearch(t *testing.T) {
	fd := &fakeDocker{body: "boot\nERROR one\nok\nerror two\n"}
	r := NewRegistry(fd)
	r.AddProvider(ProviderFunc(func(context.Context) ([]Source, error) {
		return []Source{{ID: "ctr-web", Label: "web", Kind: KindDocker, Container: "pb_web"}}, nil
	}))
	var tailed []string
	if err := r.Tail(context.Background(), "ctr-web", TailOptions{Lines: 7, Follow: true}, func(l string) error {
		tailed = append(tailed, l)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(tailed) != 4 || fd.gotName.Load() != "pb_web" || fd.opts.Load() != (DockerOptions{Tail: 7, Follow: true}) {
		t.Fatalf("tail: %v %v %v", tailed, fd.gotName.Load(), fd.opts.Load())
	}
	var hits []Match
	st, err := r.Search(context.Background(), "ctr-web", Query{Pattern: "error", CaseInsensitive: true}, SearchOptions{}, func(m Match) error {
		hits = append(hits, m)
		return nil
	})
	if err != nil || len(hits) != 2 || hits[1].Line != 4 || hits[0].File != "pb_web" || st.Files != 1 {
		t.Fatalf("search: %v %+v %+v", err, hits, st)
	}

	none := NewRegistry(nil)
	_ = none.Add(Source{ID: "c", Label: "c", Kind: KindDocker, Container: "x"})
	if err := none.Tail(context.Background(), "c", TailOptions{}, nil); !errors.Is(err, ErrUnavailable) {
		t.Errorf("no docker client: %v", err)
	}
}

func TestSourceJSONHidesPaths(t *testing.T) {
	b, err := jsonMarshal(Source{ID: "a", Label: "A", Kind: KindApp, Path: "/www/secret/a.log"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b, "secret") || strings.Contains(b, "Path") {
		t.Fatalf("path leaked: %s", b)
	}
}
