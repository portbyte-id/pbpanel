package nginx

import (
	"context"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
)

// The candidate check is `nginx -t` in a one-shot container of the candidate
// image. Exec into the running container would test the old binary.
func TestCandidateIsCheckedInItsOwnContainer(t *testing.T) {
	eng := &fakeEngine{container: running(), run: docker.ExecResult{Stderr: []byte("nginx: test is successful\n")}}
	if _, err := checkCandidate(context.Background(), eng, testPaths, "panel/nginx:1.29-new"); err != nil {
		t.Fatalf("checkCandidate = %v", err)
	}
	for _, c := range eng.calls {
		if strings.HasPrefix(c, "exec ") {
			t.Errorf("the candidate was checked by exec into the running container: %v", eng.calls)
		}
	}
	found := false
	for _, c := range eng.calls {
		if strings.HasPrefix(c, "run panel-nginx-validate nginx -t") && strings.Contains(c, "binds=") {
			found = true
		}
	}
	if !found {
		t.Errorf("no one-shot check of the candidate: %v", eng.calls)
	}
}

// The first build is made while the panel is still writing nginx's include
// files: a config that points at them cannot be checked yet, so the check does
// not run, and the apply that writes the files validates the configuration.
func TestFirstBuildIsNotCheckedAgainstAConfigThatIsNotWrittenYet(t *testing.T) {
	eng := &fakeEngine{run: docker.ExecResult{ExitCode: 1, Stderr: []byte("nginx: [emerg] open() \"/www/panel/nginx/panel-main.conf\" failed\n")}}
	imgs, err := NewImages(eng, testPaths, images.NewCatalog(openStore(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := imgs.Build(context.Background(), nil); err != nil {
		t.Fatalf("first build = %v; nothing serves, so nothing should be checked", err)
	}
	for _, c := range eng.calls {
		if strings.HasPrefix(c, "run panel-nginx-validate") {
			t.Errorf("the first build ran nginx -t: %v", eng.calls)
		}
	}
}

func TestRejectedUpdateKeepsTheBuildThatServes(t *testing.T) {
	eng := &fakeEngine{run: docker.ExecResult{Stderr: []byte("nginx: test is successful\n")}}
	imgs, err := NewImages(eng, testPaths, images.NewCatalog(openStore(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := imgs.Build(context.Background(), nil)
	if err != nil {
		t.Fatalf("first build = %v", err)
	}

	eng.run = docker.ExecResult{ExitCode: 1, Stderr: []byte("nginx: [emerg] unknown directive \"bogus\"\n")}
	if _, err := imgs.Build(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "rejected the build") {
		t.Fatalf("update with a broken build = %v", err)
	}
	cur, ok, err := imgs.Current(context.Background())
	if err != nil || !ok || cur.ID != first.ID {
		t.Errorf("current after a rejected update = %+v (ok=%v err=%v), want %s", cur, ok, err, first.ID)
	}
	builds, _ := imgs.Builds(context.Background())
	if builds[0].State != images.StateFailed || !strings.Contains(builds[0].Output, "unknown directive") {
		t.Errorf("rejected update = %+v", builds[0])
	}
}

func TestRollbackPutsThePreviousBuildBack(t *testing.T) {
	eng := &fakeEngine{run: docker.ExecResult{Stderr: []byte("ok\n")}}
	imgs, err := NewImages(eng, testPaths, images.NewCatalog(openStore(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, _ := imgs.Build(ctx, nil)
	second, _ := imgs.Build(ctx, nil)

	if _, err := imgs.Activate(ctx, first.ID); err != nil {
		t.Fatalf("rollback = %v", err)
	}
	cur, _, _ := imgs.Current(ctx)
	if cur.ID != first.ID {
		t.Errorf("after rollback, current = %s, want %s", cur.ID, first.ID)
	}
	builds, _ := imgs.Builds(ctx)
	for _, b := range builds {
		if b.ID == second.ID && !b.Previous {
			t.Error("the build rolled back from is not kept as previous")
		}
	}
}

// A stopped nginx has nothing to swap: the boot reconcile starts it on the build
// that serves. A build must not create or remove the stopped container.
func TestSwapLeavesAStoppedNginxAlone(t *testing.T) {
	eng := &fakeEngine{run: docker.ExecResult{Stderr: []byte("ok\n")}}
	imgs, err := NewImages(eng, testPaths, images.NewCatalog(openStore(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := imgs.Build(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := imgs.Build(ctx, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range eng.calls {
		if c == "remove "+ContainerName {
			t.Errorf("a build removed the stopped container: %v", eng.calls)
		}
	}
}

func TestImageFuncRefusesBeforeTheFirstBuild(t *testing.T) {
	imgs, err := NewImages(&fakeEngine{}, testPaths, images.NewCatalog(openStore(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := imgs.ImageFunc()(context.Background()); err != ErrNoImage {
		t.Errorf("image before any build = %v, want ErrNoImage", err)
	}
}
