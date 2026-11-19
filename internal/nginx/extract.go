package nginx

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxBuildFile bounds one file read out of an archive. The largest thing in a
// build is the nginx binary, tens of megabytes; this only stops a runaway stream.
const maxBuildFile = 512 << 20

// ExtractBuild unpacks the archive of a finished build into versionsDir/<id>. The
// archive is the Engine's copy of the build's directory, so its one top entry is
// <id>. The tree is written beside the target and renamed into place, so a build
// is either there whole or not at all, and an existing build is never overwritten.
//
// An entry may be a directory, a regular file or a symbolic link that stays inside
// the build. Anything else, an absolute name or one that climbs out is an error:
// the archive is the output of a build the panel ran, but it is still unpacked as
// root.
func ExtractBuild(r io.Reader, versionsDir, id string) error {
	if err := os.MkdirAll(versionsDir, 0o755); err != nil {
		return err
	}
	final := filepath.Join(versionsDir, id)
	if _, err := os.Lstat(final); err == nil {
		return fmt.Errorf("nginx: build %s already exists", id)
	}
	stage := filepath.Join(versionsDir, ".extract-"+id)
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(stage)
		}
	}()

	tr := tar.NewReader(r)
	var links []linkEntry
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("nginx: read the build archive: %w", err)
		}
		name, err := archiveName(hdr.Name, id)
		if err != nil {
			return err
		}
		if name == "" {
			continue // the top directory itself
		}
		dest := filepath.Join(stage, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(hdr.Mode).Perm())
			if err != nil {
				return err
			}
			n, err := io.Copy(f, io.LimitReader(tr, maxBuildFile+1))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			if n > maxBuildFile {
				return fmt.Errorf("nginx: %s in the build archive is larger than %d bytes", name, maxBuildFile)
			}
			// The mode may carry bits the umask hid at creation.
			if err := os.Chmod(dest, os.FileMode(hdr.Mode).Perm()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			links = append(links, linkEntry{name: name, target: hdr.Linkname})
		default:
			return fmt.Errorf("nginx: the build archive holds %s of type %q, which is not allowed", name, string(hdr.Typeflag))
		}
	}
	// Links last: a link can point at a file that appears later in the archive.
	for _, l := range links {
		if err := checkLink(l); err != nil {
			return err
		}
		dest := filepath.Join(stage, filepath.FromSlash(l.name))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.Symlink(l.target, dest); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(stage, "sbin", "nginx")); err != nil {
		return fmt.Errorf("nginx: the build archive holds no sbin/nginx: %w", err)
	}
	// The version the build compiled. A series build looks up its patch release
	// while it runs, so this file is the only record of what is actually here.
	if _, err := os.Stat(filepath.Join(stage, "version")); err != nil {
		return fmt.Errorf("nginx: the build archive holds no version: %w", err)
	}
	if err := os.Rename(stage, final); err != nil {
		return err
	}
	ok = true
	return nil
}

// BuildVersion is the nginx release a build holds, as the build recorded it.
func BuildVersion(p Paths, id string) (string, error) {
	b, err := os.ReadFile(filepath.FromSlash(p.VersionFile(id)))
	if err != nil {
		return "", fmt.Errorf("nginx: read the version of build %s: %w", id, err)
	}
	return strings.TrimSpace(string(b)), nil
}

type linkEntry struct{ name, target string }

// archiveName turns an entry name into a path inside the build: the leading <id>
// is dropped, and a name that is absolute or climbs out is refused.
func archiveName(raw, id string) (string, error) {
	n := path.Clean(strings.TrimPrefix(raw, "./"))
	if path.IsAbs(n) || n == ".." || strings.HasPrefix(n, "../") {
		return "", fmt.Errorf("nginx: the build archive names %q outside the build", raw)
	}
	if n == id || n == "." {
		return "", nil
	}
	rest, ok := strings.CutPrefix(n, id+"/")
	if !ok {
		return "", fmt.Errorf("nginx: the build archive holds %q, not under %s", raw, id)
	}
	return rest, nil
}

// checkLink accepts a relative link whose target stays inside the build.
func checkLink(l linkEntry) error {
	if path.IsAbs(l.target) {
		return fmt.Errorf("nginx: link %s points at the absolute path %s", l.name, l.target)
	}
	resolved := path.Join(path.Dir(l.name), l.target)
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("nginx: link %s points outside the build (%s)", l.name, l.target)
	}
	return nil
}
