package confedit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

const (
	// pendingDir, under the history directory, holds one directory per apply
	// that is in flight.
	pendingDir   = ".pending"
	manifestName = "manifest.json"
	// journalMode keeps the journal root-only: it holds config content, which
	// for some services includes upstream addresses and paths.
	journalDirMode  fs.FileMode = 0o700
	journalFileMode fs.FileMode = 0o600
)

// journalFile is the on-disk form of one snapshot.
type journalFile struct {
	Path     string `json:"path"`
	Existed  bool   `json:"existed"`
	Mode     uint32 `json:"mode"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
	HasOwner bool   `json:"has_owner"`
	// Blob is the name of the file in the journal directory holding the
	// previous content; empty when the file did not exist.
	Blob string `json:"blob,omitempty"`
}

type journalManifest struct {
	Files []journalFile `json:"files"`
	// Dirs are the directories the apply creates, deepest first.
	Dirs []string `json:"dirs"`
}

// writeJournal persists the pre-apply state and returns the journal directory.
//
// The manifest is written last and atomically, so its presence means the
// journal is complete. A journal without one belongs to an apply that died
// before it wrote any managed file, and Recover simply discards it.
func (e *Editor) writeJournal(snaps []snapshot, newDirs []string) (string, error) {
	var idBytes [8]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return "", err
	}
	id := e.now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(idBytes[:])
	dir := filepath.Join(e.historyDir, pendingDir, id)
	if err := os.MkdirAll(dir, journalDirMode); err != nil {
		return "", err
	}

	m := journalManifest{Dirs: newDirs}
	if m.Dirs == nil {
		m.Dirs = []string{}
	}
	for i, s := range snaps {
		jf := journalFile{Path: s.path, Existed: s.existed, Mode: uint32(s.mode), UID: s.owner.uid, GID: s.owner.gid, HasOwner: s.owner.valid}
		if s.existed {
			jf.Blob = strconv.Itoa(i)
			if err := writeAtomic(filepath.Join(dir, jf.Blob), s.content, journalFileMode, owner{}); err != nil {
				_ = os.RemoveAll(dir)
				return "", err
			}
		}
		m.Files = append(m.Files, jf)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := writeAtomic(filepath.Join(dir, manifestName), raw, journalFileMode, owner{}); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func readJournal(dir string) ([]snapshot, []string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, nil, err
	}
	var m journalManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", manifestName, err)
	}
	snaps := make([]snapshot, 0, len(m.Files))
	for _, jf := range m.Files {
		s := snapshot{
			path: jf.Path, existed: jf.Existed, mode: fs.FileMode(jf.Mode).Perm(),
			owner: owner{uid: jf.UID, gid: jf.GID, valid: jf.HasOwner},
		}
		if jf.Existed {
			// Base: the blob name comes from a file on disk and must not be
			// able to point outside the journal directory.
			if s.content, err = os.ReadFile(filepath.Join(dir, filepath.Base(jf.Blob))); err != nil {
				return nil, nil, err
			}
		}
		snaps = append(snaps, s)
	}
	return snaps, m.Dirs, nil
}

// Recover undoes every apply that was in flight when the daemon last stopped,
// and returns the paths it put back. Call it once at boot, before any service
// is reloaded: a file from an unfinished apply was never validated.
//
// It reloads nothing. Boot reconciliation reloads each service once after it.
func (e *Editor) Recover() ([]string, error) {
	root := filepath.Join(e.historyDir, pendingDir)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("confedit: recover: %w", err)
	}

	var (
		restored []string
		errs     []error
	)
	for _, entry := range entries {
		dir := filepath.Join(root, entry.Name())
		if !entry.IsDir() {
			continue
		}
		snaps, newDirs, err := readJournal(dir)
		if errors.Is(err, fs.ErrNotExist) {
			// No manifest: the apply died while journaling, before it touched
			// a managed file.
			if err := os.RemoveAll(dir); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("journal %s: %w", entry.Name(), err))
			continue
		}

		failed := false
		for _, s := range snaps {
			// The journal is trusted as far as its paths stay inside the
			// managed roots: it is the editor's own file, but a restore is a
			// root write and gets the same check as any other.
			if err := e.checkPath(s.path); err != nil {
				errs = append(errs, fmt.Errorf("journal %s: %w", entry.Name(), err))
				failed = true
				continue
			}
			if err := s.restore(e.write); err != nil {
				errs = append(errs, fmt.Errorf("journal %s: restore %s: %w", entry.Name(), s.path, err))
				failed = true
				continue
			}
			restored = append(restored, s.path)
		}
		for _, d := range newDirs {
			if e.checkPath(filepath.Join(d, "x")) != nil {
				continue
			}
			// Best effort: a directory that gained other files since is left.
			_ = os.Remove(d)
		}
		if !failed {
			if err := os.RemoveAll(dir); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) > 0 {
		return restored, fmt.Errorf("confedit: recover: %w", errors.Join(errs...))
	}
	return restored, nil
}
