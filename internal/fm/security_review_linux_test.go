//go:build linux

package fm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Regression tests from the M3 step 3.11 security review. Each one failed
// against the code it pins before the fix.

// TestUploadChunkRefusesSwappedPartSymlink pins: UploadChunk opens the .part
// file without O_NOFOLLOW, so a symlink swapped in for the part file redirects
// the chunk's bytes to any file on disk.
func TestUploadChunkRefusesSwappedPartSymlink(t *testing.T) {
	e := newEnv(t)
	victim := e.write("victim.txt", "safe", 0o644)
	u, err := e.svc.UploadStart(bg, e.work, "f.bin", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	part := e.svc.partPath(u.ID)
	if err := os.Remove(part); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, part); err != nil {
		t.Fatal(err)
	}
	chunk := []byte("0123456789")
	sum := sha256.Sum256(chunk)
	if _, err := e.svc.UploadChunk(bg, u.ID, 0, hex.EncodeToString(sum[:]), bytes.NewReader(chunk)); err == nil {
		t.Fatal("UploadChunk followed a symlink swapped in for the .part file")
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "safe" {
		t.Fatalf("victim file was written through the swapped link: %q %v", b, err)
	}
}

// TestInternalTreeOffLimitsToGenericOperations pins: the trash and uploads
// trees are documented as touchable only by the Trash and Upload APIs, but
// Touch, Mkdir, Move and Copy never consult internalTree, so bytes can land in
// the bin (or the uploads directory) without an index row.
func TestInternalTreeOffLimitsToGenericOperations(t *testing.T) {
	e := newEnv(t)
	src := e.write("x.txt", "x", 0o644)
	uploads := filepath.Join(e.panel, "uploads")
	trash := filepath.Join(e.panel, "trash")
	if err := e.svc.Touch(filepath.Join(trash, "stray"), 0o644); !errors.Is(err, ErrProtected) {
		t.Fatalf("Touch in the trash tree = %v, want ErrProtected", err)
	}
	if err := e.svc.Touch(filepath.Join(uploads, "stray"), 0o644); !errors.Is(err, ErrProtected) {
		t.Fatalf("Touch in the uploads tree = %v, want ErrProtected", err)
	}
	if err := e.svc.Move(bg, src, uploads, nil); !errors.Is(err, ErrProtected) {
		t.Fatalf("Move into the uploads tree = %v, want ErrProtected", err)
	}
}

// TestUploadCompleteRefusesInternalTreeAfterDirSwap pins: UploadComplete
// re-judges the target directory but checks only CheckWrite, not internalTree.
// A directory swapped for the upload's target between start and complete puts
// the finished file inside the panel's uploads tree without a row.
func TestUploadCompleteRefusesInternalTreeAfterDirSwap(t *testing.T) {
	e := newEnv(t)
	sub := filepath.Join(e.work, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	u, err := e.svc.UploadStart(bg, sub, "f.bin", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	// u.Dir is the resolved real path of sub. Move the directory away and put
	// a link to the uploads tree in its place.
	if err := os.Rename(sub, sub+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.panel, "uploads"), u.Dir); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UploadComplete(bg, u.ID, ""); !errors.Is(err, ErrProtected) {
		t.Fatalf("UploadComplete through a directory swapped to the uploads tree = %v, want ErrProtected", err)
	}
	if _, err := os.Stat(filepath.Join(e.panel, "uploads", "f.bin")); err == nil {
		t.Fatal("the finished upload landed inside the panel's uploads tree")
	}
}
