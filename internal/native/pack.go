package native

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// PackBuild writes the build directory dir as a gzip archive at out and returns
// its sha256 and size. The archive's one top entry is id, which is what
// ExtractBuild expects, so a package made here unpacks with the same checks a
// build read out of a container gets.
//
// Ownership and times are not recorded: ExtractBuild ignores them, and leaving
// them out keeps two packs of the same tree the same size.
func PackBuild(dir, id, out string) (digest string, size int64, err error) {
	f, err := os.OpenFile(filepath.Clean(out), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", 0, err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(out)
		}
	}()
	sum := sha256.New()
	counter := &countWriter{w: io.MultiWriter(f, sum)}
	gz, err := gzip.NewWriterLevel(counter, gzip.BestCompression)
	if err != nil {
		return "", 0, err
	}
	tw := tar.NewWriter(gz)

	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		name := id
		if rel != "." {
			name = path.Join(id, filepath.ToSlash(rel))
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: name, Mode: int64(info.Mode().Perm())}
		switch {
		case info.IsDir():
			hdr.Typeflag = tar.TypeDir
			hdr.Name += "/"
		case info.Mode().IsRegular():
			hdr.Typeflag = tar.TypeReg
			hdr.Size = info.Size()
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = target
		default:
			return fmt.Errorf("native: %s is neither a file, a directory nor a link", p)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil
		}
		src, err := os.Open(filepath.Clean(p))
		if err != nil {
			return err
		}
		defer func() { _ = src.Close() }()
		_, err = io.Copy(tw, src)
		return err
	})
	if walkErr != nil {
		return "", 0, walkErr
	}
	if err := tw.Close(); err != nil {
		return "", 0, err
	}
	if err := gz.Close(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(sum.Sum(nil)), counter.n, nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}
