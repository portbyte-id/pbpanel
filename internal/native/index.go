package native

import (
	"sort"
	"strings"
)

// ManifestName is the name of the manifest that describes the package file: the
// file name with .tar.gz replaced by .manifest.json.
func ManifestName(file string) string {
	return strings.TrimSuffix(file, ".tar.gz") + ".manifest.json"
}

// BuildIndex merges the packages a CI job has uploaded into the index the panel
// reads, and names the assets to delete so a release does not grow without end.
//
// Packages are grouped by product and series for one distro release, architecture
// and install root. The newest keep of each group are published, newest first; the
// rest are dropped, a package and its manifest each. A new recipe therefore
// replaces the old one over time, while the build a host is likely to be serving
// stays published. A package whose record is malformed is left out and never
// dropped: it is not this code's to delete.
func BuildIndex(pkgs []Package, keep int) (Index, []string) {
	if keep < 1 {
		keep = 1
	}
	groups := map[string][]Package{}
	for _, p := range pkgs {
		if !p.valid() {
			continue
		}
		key := strings.Join([]string{p.Product, series(p.Release), p.Distro, p.VersionID, p.Arch, p.Root}, "\x00")
		groups[key] = append(groups[key], p)
	}

	idx := Index{Version: IndexVersion, Packages: []Package{}}
	var drop []string
	for _, g := range groups {
		sort.Slice(g, func(i, j int) bool { return later(g[i], g[j]) })
		cut := min(keep, len(g))
		idx.Packages = append(idx.Packages, g[:cut]...)
		for _, p := range g[cut:] {
			drop = append(drop, p.File, ManifestName(p.File))
		}
	}
	sort.Slice(idx.Packages, func(i, j int) bool { return later(idx.Packages[i], idx.Packages[j]) })
	sort.Strings(drop)
	return idx, drop
}

// series is the first two numbers of a release: 1.30.2 belongs to 1.30.
func series(release string) string {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return release
	}
	return parts[0] + "." + parts[1]
}

// later reports whether a was built after b; the ID breaks a tie.
func later(a, b Package) bool {
	if a.Built != b.Built {
		return a.Built > b.Built
	}
	return a.ID > b.ID
}
