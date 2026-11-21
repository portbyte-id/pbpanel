// Command mkruntime builds one prebuilt runtime package for the panel to download:
// nginx compiled for one distro release and one architecture, under the install
// root the panel uses. A CI job runs it; it is not part of the shipped binary.
//
// It runs the same recipe a host would, through the same code, on a throwaway
// builder image that matches the distro asked for, then packs the finished build
// and writes a manifest beside it. The manifest is one record of the index the
// panel reads (internal/native.Package); the index subcommand merges them.
//
//	mkruntime build -distro ubuntu -version-id 24.04 -series 1.30 -out out
//	mkruntime index -manifests manifests -out index.json -drop drop.txt
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
	"github.com/portbyte-id/server-panel/internal/native"
	"github.com/portbyte-id/server-panel/internal/nginx"
)

var wordRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mkruntime:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "build":
			return build(ctx, args[1:])
		case "index":
			return index(args[1:])
		}
	}
	return errors.New("usage: mkruntime build -distro D -version-id V -out DIR | mkruntime index -manifests DIR -out FILE -drop FILE")
}

// index merges the manifests a set of build jobs uploaded into index.json, and
// writes the names of the assets that have been superseded, one per line.
func index(args []string) error {
	fs := flag.NewFlagSet("mkruntime index", flag.ContinueOnError)
	dir := fs.String("manifests", "", "directory holding the *.manifest.json files")
	out := fs.String("out", "", "where index.json is written")
	drop := fs.String("drop", "", "where the list of superseded assets is written")
	keep := fs.Int("keep", 2, "packages kept per product, series, distro release, architecture and root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *out == "" || *drop == "" || *keep < 1 {
		return errors.New("-manifests, -out and -drop are required, and -keep must be at least 1")
	}
	files, err := filepath.Glob(filepath.Join(*dir, "*.manifest.json"))
	if err != nil {
		return err
	}
	var pkgs []native.Package
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var p native.Package
		if err := json.Unmarshal(raw, &p); err != nil {
			fmt.Fprintf(os.Stderr, "mkruntime: skipping %s: %v\n", filepath.Base(f), err)
			continue
		}
		pkgs = append(pkgs, p)
	}
	idx, dropped := native.BuildIndex(pkgs, *keep)
	if len(idx.Packages) == 0 {
		return fmt.Errorf("no usable manifest in %s; refusing to publish an empty index", *dir)
	}
	body, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, append(body, '\n'), 0o644); err != nil {
		return err
	}
	list := strings.Join(dropped, "\n")
	if list != "" {
		list += "\n"
	}
	if err := os.WriteFile(*drop, []byte(list), 0o644); err != nil {
		return err
	}
	fmt.Printf("index: %d packages published, %d assets superseded\n", len(idx.Packages), len(dropped))
	return nil
}

// build compiles one package and writes it with its manifest.
func build(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mkruntime build", flag.ContinueOnError)
	product := fs.String("product", "nginx", "what to build; only nginx is published yet")
	distro := fs.String("distro", "", "distro the package is for: ubuntu or debian")
	versionID := fs.String("version-id", "", "release of that distro, as in /etc/os-release VERSION_ID (24.04, 12)")
	series := fs.String("series", nginx.Version, "version to build: a series (1.30) or one release (1.30.2)")
	root := fs.String("root", "/www", "install root the panel will run under; compiled into the build's paths")
	out := fs.String("out", "", "directory the package and its manifest are written to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case *product != "nginx":
		return fmt.Errorf("product %q is not published yet", *product)
	case !wordRe.MatchString(*distro) || !wordRe.MatchString(*versionID):
		return errors.New("-distro and -version-id are required, for example -distro ubuntu -version-id 24.04")
	case *out == "":
		return errors.New("-out is required")
	case !path.IsAbs(*root) || path.Clean(*root) != *root:
		return fmt.Errorf("-root %q must be a clean absolute path", *root)
	}
	if err := nginx.CheckVersion(*series); err != nil {
		return err
	}

	work, err := os.MkdirTemp("", "mkruntime-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	// The builder image is chosen from this file, as it is on a host, so the
	// package is compiled on the distro it is for whatever the runner runs.
	osRelease := filepath.Join(work, "os-release")
	if err := os.WriteFile(osRelease, fmt.Appendf(nil, "ID=%s\nVERSION_ID=%q\n", *distro, *versionID), 0o600); err != nil {
		return err
	}
	versions := filepath.Join(work, "versions")
	spec := nginx.BuildSpec(nginx.Paths{Root: *root})
	spec.VersionsDir = func() string { return versions }

	eng, err := docker.New(docker.DefaultSocket)
	if err != nil {
		return err
	}
	id, err := images.NewID(time.Now())
	if err != nil {
		return err
	}
	b := native.DockerBuilder{Eng: eng, OSRelease: osRelease, Spec: spec}
	if err := b.Build(ctx, id, *series, func(line string) { fmt.Println(line) }); err != nil {
		return err
	}

	dir := filepath.Join(versions, id)
	release, err := native.BuildVersion(filepath.Join(dir, "version"), id, *product)
	if err != nil {
		return err
	}
	if !native.ReleaseMatches(*series, release) {
		return fmt.Errorf("asked for nginx %s but the build holds %s", *series, release)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	file := fmt.Sprintf("%s-%s-%s%s-%s-%s.tar.gz", *product, release, *distro, *versionID, runtime.GOARCH, id)
	digest, size, err := native.PackBuild(dir, id, filepath.Join(*out, file))
	if err != nil {
		return err
	}
	pkg := native.Package{
		Product: *product, Release: release, ID: id,
		Distro: *distro, VersionID: *versionID, Arch: runtime.GOARCH,
		Root: *root, Recipe: nginx.RecipeID(),
		File: file, SHA256: digest, Size: size,
		Built: time.Now().UTC().Format(time.RFC3339),
	}
	manifest, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return err
	}
	name := strings.TrimSuffix(file, ".tar.gz") + ".manifest.json"
	if err := os.WriteFile(filepath.Join(*out, name), append(manifest, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d bytes, sha256 %s)\n", file, size, digest)
	return nil
}
