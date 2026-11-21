package nginx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"sync"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/native"
)

// RecipeID names the recipe a build is made from: the build script and everything
// it pins (the OpenSSL series, ModSecurity, the Core Rule Set and its digest).
// It is a hash of the rendered recipe, so it changes whenever any of that does and
// nobody has to remember to bump it. A prebuilt package carries the ID it was made
// from, and a host takes only a package of its own recipe: an older one may lack
// a module the panel now expects.
//
// The version, build ID and root are fixed placeholders here. They vary per build
// and are matched separately.
func RecipeID() string {
	sum := sha256.Sum256(DockerfileFor("base", "0", "1.0", Paths{Root: "/www"}))
	return hex.EncodeToString(sum[:])[:16]
}

// IDPicker is a Builder that can say, before a build starts, which ID it will
// build under. A prebuilt package was compiled with its ID in its prefix, so the
// host has to keep it under that ID rather than a generated one.
type IDPicker interface {
	// PickID returns the ID to build version under, or false to take a generated
	// one. exists reports whether the catalogue already holds an ID.
	PickID(ctx context.Context, version string, exists func(id string) bool) (string, bool)
}

// Prebuilt is a Builder that takes a build a CI job already compiled when one
// fits this host, and compiles with Fallback when none does.
//
// A package fits when it was made for this distro, release and architecture (it
// links the host's own glibc), under this install root (the paths are compiled
// in), from this recipe, and holds the nginx version asked for. Whatever the
// reason a package is not used, a missing index, a digest that does not match, an
// archive that fails ExtractBuild's checks, the host builds from source: this is a
// shortcut, never a requirement.
type Prebuilt struct {
	// Fallback builds from source. Required.
	Fallback Builder
	P        Paths
	// Enabled and URL are read at the start of every build, so a change on the
	// Settings page applies to the next one. A nil Enabled means off.
	Enabled func(ctx context.Context) bool
	URL     func(ctx context.Context) string

	// OSRelease is the file the host is matched on; empty means /etc/os-release.
	OSRelease string
	// Arch defaults to the architecture this daemon runs on, which is the one the
	// builder image would be pulled for.
	Arch string
	// Client defaults to the one package native supplies.
	Client *http.Client

	mu    sync.Mutex
	plans map[string]native.Package
	why   string
}

func (b *Prebuilt) source(ctx context.Context) native.Source {
	url := ""
	if b.URL != nil {
		url = b.URL(ctx)
	}
	return native.Source{BaseURL: url, Client: b.Client}
}

// PickID looks for a package that fits and returns its ID. It remembers the
// package for Build and, when it takes none, why, so the build's progress can say.
func (b *Prebuilt) PickID(ctx context.Context, version string, exists func(id string) bool) (string, bool) {
	b.mu.Lock()
	b.why = ""
	b.mu.Unlock()
	if b.Enabled == nil || !b.Enabled(ctx) {
		return "", false
	}
	pkg, err := b.find(ctx, version)
	if err != nil {
		b.setWhy(err.Error())
		return "", false
	}
	if exists(pkg.ID) {
		b.setWhy(fmt.Sprintf("nginx %s, the newest one published, is already on this host", pkg.Release))
		return "", false
	}
	b.mu.Lock()
	if b.plans == nil {
		b.plans = map[string]native.Package{}
	}
	b.plans[pkg.ID] = pkg
	b.mu.Unlock()
	return pkg.ID, true
}

func (b *Prebuilt) find(ctx context.Context, version string) (native.Package, error) {
	file := b.OSRelease
	if file == "" {
		file = "/etc/os-release"
	}
	host, err := native.ReadHost(file, product)
	if err != nil {
		return native.Package{}, err
	}
	arch := b.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	return b.source(ctx).Find(ctx, native.Want{
		Product: product, Version: version, Host: host, Arch: arch,
		Root: b.P.Root, Recipe: RecipeID(),
	})
}

func (b *Prebuilt) setWhy(s string) {
	b.mu.Lock()
	b.why = s
	b.mu.Unlock()
}

// take returns the package chosen for id, once.
func (b *Prebuilt) take(id string) (native.Package, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.plans[id]
	delete(b.plans, id)
	return p, ok
}

func (b *Prebuilt) takeWhy() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.why
	b.why = ""
	return w
}

// Build unpacks the package PickID chose for id, or compiles. A package that
// cannot be used falls back to compiling under the same ID: the recipe takes
// whatever ID it is given.
func (b *Prebuilt) Build(ctx context.Context, id, version string, progress docker.Progress) error {
	if b.Fallback == nil {
		return errors.New("nginx: the prebuilt builder has no fallback")
	}
	pkg, ok := b.take(id)
	if !ok {
		if why := b.takeWhy(); why != "" {
			tell(progress, "no prebuilt package used: "+why)
			tell(progress, "building from source")
		}
		return b.Fallback.Build(ctx, id, version, progress)
	}
	tell(progress, fmt.Sprintf("using the prebuilt nginx %s made for %s %s (%s)", pkg.Release, pkg.Distro, pkg.VersionID, pkg.Arch))
	err := b.source(ctx).Fetch(ctx, pkg, b.P.VersionsDir(), buildMarkers, progress)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return err
	}
	tell(progress, "the prebuilt package could not be used: "+err.Error())
	tell(progress, "building from source instead")
	return b.Fallback.Build(ctx, id, version, progress)
}

func tell(p docker.Progress, line string) {
	if p != nil {
		p(line)
	}
}
