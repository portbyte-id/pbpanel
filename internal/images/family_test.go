package images

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/store"
)

var bg = context.Background()

// fakeImages is an image store holding the tags of the family's builds.
type fakeImages struct {
	mu        sync.Mutex
	have      map[string]docker.Image
	removeErr error
}

func (f *fakeImages) ImageInspect(_ context.Context, ref string) (docker.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if img, ok := f.have[ref]; ok {
		return img, nil
	}
	return docker.Image{}, docker.ErrNotFound
}

func (f *fakeImages) ImageTag(_ context.Context, src, dst string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	img, ok := f.have[src]
	if !ok {
		return docker.ErrNotFound
	}
	f.have[dst] = img
	return nil
}

func (f *fakeImages) ImageRemove(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removeErr != nil {
		return f.removeErr
	}
	if _, ok := f.have[ref]; !ok {
		return docker.ErrNotFound
	}
	delete(f.have, ref)
	return nil
}

func (f *fakeImages) has(ref string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.have[ref]
	return ok
}

// rig is a PHP family over fake hooks that can be made to fail.
type rig struct {
	fam       *Family
	imgs      *fakeImages
	cat       *Catalog
	buildErr  error
	checkErr  error
	checkOut  string
	swapErr   error
	swaps     [][2]string
	buildHold chan struct{}
	clock     time.Time
	// checkedServing records, for each check, whether a build served at the time.
	checkedServing []bool
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := &rig{imgs: &fakeImages{have: map[string]docker.Image{}}, cat: NewCatalog(st), clock: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	fam, err := NewFamily(FamilyOptions{
		Kind: KindPHP, Version: "8.3", Repo: "panel/php", Catalog: r.cat, Engine: r.imgs,
		Hooks: Hooks{
			Build: func(_ context.Context, b Build, progress docker.Progress) error {
				if r.buildHold != nil {
					<-r.buildHold
				}
				progress("Step 1/2 for " + b.ID)
				if r.buildErr != nil {
					return r.buildErr
				}
				r.imgs.mu.Lock()
				r.imgs.have[b.Tag] = docker.Image{ID: "sha256:" + b.ID}
				r.imgs.mu.Unlock()
				return nil
			},
			Check: func(_ context.Context, _ Build, serving bool) (string, error) {
				r.checkedServing = append(r.checkedServing, serving)
				return r.checkOut, r.checkErr
			},
			Swap: func(_ context.Context, from, to string) error {
				r.swaps = append(r.swaps, [2]string{from, to})
				return r.swapErr
			},
		},
		// Each call is a second later, so "newest first" is well defined.
		Now: func() time.Time {
			r.clock = r.clock.Add(time.Second)
			return r.clock
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.fam = fam
	return r
}

func (r *rig) build(t *testing.T) Build {
	t.Helper()
	b, err := r.fam.Build(bg, nil, nil)
	if err != nil {
		t.Fatalf("Build = %v", err)
	}
	return b
}

// The check is told whether anything serves: a first build has nothing to
// protect, and a later one does.
func TestCheckIsToldWhetherAnythingServes(t *testing.T) {
	r := newRig(t)
	r.build(t)
	r.build(t)
	if len(r.checkedServing) != 2 || r.checkedServing[0] || !r.checkedServing[1] {
		t.Errorf("serving flags = %v, want [false true]", r.checkedServing)
	}
}

func TestBuildPromotesAndKeepsTwoBuilds(t *testing.T) {
	r := newRig(t)
	a := r.build(t)
	b := r.build(t)
	c := r.build(t)

	cur, ok, err := r.fam.Current(bg)
	if err != nil || !ok || cur.ID != c.ID {
		t.Fatalf("current = %+v ok=%v err=%v, want %s", cur, ok, err, c.ID)
	}
	if !r.imgs.has(c.Tag) || !r.imgs.has(b.Tag) {
		t.Error("the current and the previous image must stay")
	}
	if r.imgs.has(a.Tag) {
		t.Error("the image of the third-last build was not removed")
	}
	builds, err := r.fam.Builds(bg)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]State{}
	for _, x := range builds {
		states[x.ID] = x.State
		if x.ID == b.ID && !x.Previous {
			t.Error("the previous build is not flagged")
		}
	}
	if states[a.ID] != StatePruned || states[b.ID] != StateReady || states[c.ID] != StateReady {
		t.Errorf("states = %v", states)
	}
	if got := r.swaps[len(r.swaps)-1]; got != [2]string{b.Tag, c.Tag} {
		t.Errorf("last swap = %v, want %s to %s", got, b.Tag, c.Tag)
	}
}

func TestBuildFailureKeepsTheServingImage(t *testing.T) {
	r := newRig(t)
	a := r.build(t)
	r.buildErr = errors.New("the extension bogus failed to build")
	if _, err := r.fam.Build(bg, []string{"bogus"}, nil); err == nil {
		t.Fatal("a failed build returned no error")
	}
	cur, _, _ := r.fam.Current(bg)
	if cur.ID != a.ID {
		t.Errorf("current = %s after a failed build, want %s", cur.ID, a.ID)
	}
	if len(r.swaps) != 1 {
		t.Errorf("swaps = %v: a failed build must not touch the service", r.swaps)
	}
	builds, _ := r.fam.Builds(bg)
	failed := builds[0]
	if failed.State != StateFailed || !strings.Contains(failed.Output, "bogus failed") || !strings.Contains(failed.Output, "Step 1/2") {
		t.Errorf("failed build = %+v", failed)
	}
}

func TestCheckFailureRemovesTheCandidate(t *testing.T) {
	r := newRig(t)
	a := r.build(t)
	r.checkErr = errors.New("php -m: intl is missing")
	r.checkOut = "PHP Warning: Unable to load dynamic library 'intl'"
	if _, err := r.fam.Build(bg, []string{"intl"}, nil); err == nil {
		t.Fatal("a rejected candidate was accepted")
	}
	builds, _ := r.fam.Builds(bg)
	cand := builds[0]
	if cand.State != StateFailed || !strings.Contains(cand.Output, "Unable to load dynamic library") {
		t.Errorf("candidate = %+v", cand)
	}
	if r.imgs.has(cand.Tag) {
		t.Error("the rejected image was left behind")
	}
	if len(r.swaps) != 1 || !r.imgs.has(a.Tag) {
		t.Error("a rejected candidate reached the service or removed the serving image")
	}
}

func TestSwapFailureKeepsThePreviousBuild(t *testing.T) {
	r := newRig(t)
	a := r.build(t)
	r.swapErr = errors.New("the new container did not come up")
	if _, err := r.fam.Build(bg, nil, nil); err == nil {
		t.Fatal("a failed swap returned no error")
	}
	cur, _, _ := r.fam.Current(bg)
	if cur.ID != a.ID {
		t.Errorf("current moved to %s after a failed swap", cur.ID)
	}
	builds, _ := r.fam.Builds(bg)
	if builds[0].State != StateFailed || r.imgs.has(builds[0].Tag) {
		t.Errorf("the candidate after a failed swap = %+v", builds[0])
	}
	if !r.imgs.has(a.Tag) {
		t.Error("the serving image was removed")
	}
}

func TestActivatePutsAnEarlierBuildBack(t *testing.T) {
	r := newRig(t)
	a := r.build(t)
	b := r.build(t)
	r.swaps = nil

	got, err := r.fam.Activate(bg, a.ID)
	if err != nil {
		t.Fatalf("Activate = %v", err)
	}
	if !got.Current {
		t.Errorf("activated build not current: %+v", got)
	}
	if r.swaps[0] != [2]string{b.Tag, a.Tag} {
		t.Errorf("swap = %v, want %s to %s", r.swaps[0], b.Tag, a.Tag)
	}
	p, _ := r.cat.Pointers(bg, KindPHP, "8.3")
	if p.Current != a.ID || p.Previous != b.ID {
		t.Errorf("pointers = %+v", p)
	}
	if _, err := r.fam.Activate(bg, a.ID); err != nil || len(r.swaps) != 1 {
		t.Errorf("activating the current build must be a no-op: err=%v swaps=%d", err, len(r.swaps))
	}
}

func TestActivateRefusesPrunedAndUnknownBuilds(t *testing.T) {
	r := newRig(t)
	a := r.build(t)
	r.build(t)
	r.build(t)
	if _, err := r.fam.Activate(bg, a.ID); !errors.Is(err, ErrNotReady) {
		t.Errorf("activating a pruned build: err = %v, want ErrNotReady", err)
	}
	if _, err := r.fam.Activate(bg, "20260101000000-000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("activating an unknown build: err = %v, want ErrNotFound", err)
	}
}

func TestBuildRefusesWhileAnotherRuns(t *testing.T) {
	r := newRig(t)
	r.buildHold = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := r.fam.Build(bg, nil, nil)
		done <- err
	}()
	// Wait until the first build holds the family.
	for r.fam.Idle() == nil {
		time.Sleep(time.Millisecond)
	}
	if _, err := r.fam.Build(bg, nil, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("second build: err = %v, want ErrBusy", err)
	}
	close(r.buildHold)
	if err := <-done; err != nil {
		t.Fatalf("first build = %v", err)
	}
	if r.fam.Idle() != nil {
		t.Error("the family is still busy after the build")
	}
}

func TestRemoveStopsAndDeletesEverything(t *testing.T) {
	r := newRig(t)
	a := r.build(t)
	b := r.build(t)
	stopped := 0
	if err := r.fam.Remove(bg, func(context.Context) error { stopped++; return nil }); err != nil {
		t.Fatalf("Remove = %v", err)
	}
	if stopped != 1 {
		t.Errorf("stop ran %d times", stopped)
	}
	if r.imgs.has(a.Tag) || r.imgs.has(b.Tag) {
		t.Error("images survived Remove")
	}
	if p, _ := r.cat.Pointers(bg, KindPHP, "8.3"); p.Current != "" || p.Previous != "" {
		t.Errorf("pointers survived Remove: %+v", p)
	}
	if _, ok, _ := r.fam.Current(bg); ok {
		t.Error("a build still serves after Remove")
	}
}

func TestRemoveKeepsImagesWhenTheServiceDoesNotStop(t *testing.T) {
	r := newRig(t)
	a := r.build(t)
	if err := r.fam.Remove(bg, func(context.Context) error { return errors.New("docker is down") }); err == nil {
		t.Fatal("Remove ignored a stop failure")
	}
	if !r.imgs.has(a.Tag) {
		t.Error("the image was removed although the service did not stop")
	}
}

func TestReconcileFailsInterruptedBuildsAndAdoptsLegacy(t *testing.T) {
	r := newRig(t)
	if err := r.cat.insert(bg, Build{ID: "20261117100000-aaaaaa", Kind: KindPHP, Version: "8.3", Tag: "panel/php:8.3-20261117100000-aaaaaa", State: StateBuilding, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	r.imgs.have["panel/php:8.3-fpm"] = docker.Image{ID: "sha256:legacy"}

	serving, err := r.fam.Reconcile(bg, "panel/php:8.3-fpm")
	if err != nil || !serving {
		t.Fatalf("Reconcile = %v, %v; want the adopted build serving", serving, err)
	}
	builds, _ := r.fam.Builds(bg)
	var adopted, interrupted Build
	for _, b := range builds {
		if b.ImageID == "sha256:legacy" {
			adopted = b
		} else {
			interrupted = b
		}
	}
	if !adopted.Current || adopted.State != StateReady || !strings.HasPrefix(adopted.Output, "adopted from") {
		t.Errorf("adopted = %+v", adopted)
	}
	if interrupted.State != StateFailed || !strings.Contains(interrupted.Output, "interrupted") {
		t.Errorf("interrupted = %+v", interrupted)
	}
	if !r.imgs.has(adopted.Tag) || !r.imgs.has("panel/php:8.3-fpm") {
		t.Error("adoption must tag the legacy image, not move it")
	}
}

func TestReconcileDropsACurrentBuildWhoseImageIsGone(t *testing.T) {
	r := newRig(t)
	a := r.build(t)
	r.imgs.mu.Lock()
	delete(r.imgs.have, a.Tag)
	r.imgs.mu.Unlock()

	serving, err := r.fam.Reconcile(bg, "")
	if err != nil || serving {
		t.Fatalf("Reconcile = %v, %v; want nothing serving", serving, err)
	}
	if p, _ := r.cat.Pointers(bg, KindPHP, "8.3"); p.Current != "" {
		t.Errorf("pointer still names the missing build: %+v", p)
	}
}

func TestNewFamilyRejectsAnIncompleteFamily(t *testing.T) {
	if _, err := NewFamily(FamilyOptions{Kind: "mysql", Version: "8.3", Repo: "x"}); err == nil {
		t.Error("an unknown kind was accepted")
	}
	if _, err := NewFamily(FamilyOptions{Kind: KindPHP, Version: "8.3", Repo: "panel/php"}); err == nil {
		t.Error("a family without catalogue, engine and hooks was accepted")
	}
}

func TestNewIDHasTheShapeTheTagsNeed(t *testing.T) {
	id, err := NewID(time.Date(2026, 11, 17, 17, 56, 1, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckID(id); err != nil {
		t.Errorf("NewID = %q: %v", id, err)
	}
	if !strings.HasPrefix(id, "20261117175601-") {
		t.Errorf("NewID = %q, want the UTC time first", id)
	}
	for _, bad := range []string{"", "latest", "20261117175601-ABCDEF", "2026-11-17", "x/../y"} {
		if CheckID(bad) == nil {
			t.Errorf("CheckID(%q) accepted", bad)
		}
	}
}
