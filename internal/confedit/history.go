package confedit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// timeLayout is fixed width, like every other timestamp column, so string
// order is time order.
const timeLayout = "2006-01-02T15:04:05.000Z"

// ErrNoVersion is returned for a path/version pair the history does not hold.
var ErrNoVersion = errors.New("confedit: no such version")

// Version is one stored version of a file.
type Version struct {
	Path      string    `json:"path"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	Reason    string    `json:"reason"`
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// blobPath is where a version's content lives. The directory is a hash of the
// managed path, so no part of that path is ever used as a file name here.
func (e *Editor) blobPath(path string, version int) string {
	return filepath.Join(e.historyDir, digest([]byte(path))[:32], strconv.Itoa(version))
}

// History lists the stored versions of path, newest first.
func (e *Editor) History(ctx context.Context, path string) ([]Version, error) {
	const q = `SELECT version, created_at, sha256, size, reason
	           FROM config_history WHERE path = ? ORDER BY version DESC`
	rows, err := e.store.DB().QueryContext(ctx, q, path)
	if err != nil {
		return nil, fmt.Errorf("confedit: history of %s: %w", path, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Version
	for rows.Next() {
		v := Version{Path: path}
		var created string
		if err := rows.Scan(&v.Version, &created, &v.SHA256, &v.Size, &v.Reason); err != nil {
			return nil, fmt.Errorf("confedit: history of %s: %w", path, err)
		}
		if v.CreatedAt, err = time.Parse(timeLayout, created); err != nil {
			return nil, fmt.Errorf("confedit: history of %s: %w", path, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("confedit: history of %s: %w", path, err)
	}
	return out, nil
}

// ReadVersion returns the content of one stored version. The content is checked
// against the recorded digest, so a damaged history file is an error rather
// than something that could be restored into a live config.
func (e *Editor) ReadVersion(ctx context.Context, path string, version int) ([]byte, error) {
	var want string
	err := e.store.DB().QueryRowContext(ctx,
		`SELECT sha256 FROM config_history WHERE path = ? AND version = ?`, path, version).Scan(&want)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s version %d", ErrNoVersion, path, version)
	}
	if err != nil {
		return nil, fmt.Errorf("confedit: read %s version %d: %w", path, version, err)
	}
	content, err := os.ReadFile(e.blobPath(path, version))
	if err != nil {
		return nil, fmt.Errorf("confedit: read %s version %d: %w", path, version, err)
	}
	if digest(content) != want {
		return nil, fmt.Errorf("confedit: %s version %d is damaged: its content does not match the recorded digest", path, version)
	}
	return content, nil
}

// Restore applies a stored version as the file's new content. It is an ordinary
// apply: the old version is validated against today's config like any edit, and
// the restore itself becomes a new history entry.
func (e *Editor) Restore(ctx context.Context, path string, version int, services []Service) (Result, error) {
	content, err := e.ReadVersion(ctx, path, version)
	if err != nil {
		return Result{}, err
	}
	return e.Apply(ctx, Change{
		Files:    []File{{Path: path, Content: content}},
		Services: services,
		Reason:   fmt.Sprintf("restore version %d", version),
	})
}

// recordHistory stores what an apply replaced and what it wrote. It runs after
// the apply has succeeded, so a failure here is a warning on the result: the
// config is live and correct, only its history entry is missing.
func (e *Editor) recordHistory(ctx context.Context, ch Change, snaps []snapshot, res *Result) {
	// The apply is done; a request that was canceled a moment ago must not
	// cost the history entry for a change that is already live.
	ctx = context.WithoutCancel(ctx)
	for i, f := range ch.Files {
		// The content found on disk is recorded when the history does not end
		// with it already: the first time a file is touched, or after it was
		// changed behind the panel's back. Without this the state an operator
		// most wants back, "before the panel edited it", would not exist.
		if snaps[i].existed {
			if _, err := e.record(ctx, f.Path, snaps[i].content, "on disk before: "+ch.Reason); err != nil {
				res.Warnings = append(res.Warnings, err.Error())
			}
		}
		if f.Delete {
			continue
		}
		v, err := e.record(ctx, f.Path, f.Content, ch.Reason)
		if err != nil {
			res.Warnings = append(res.Warnings, err.Error())
			continue
		}
		res.Versions[f.Path] = v
	}
}

// record appends content as the newest version of path, unless the newest
// version already is that content, and trims the history to HistoryVersions.
// It returns the version number holding the content.
func (e *Editor) record(ctx context.Context, path string, content []byte, reason string) (int, error) {
	sum := digest(content)
	var (
		latest    int
		latestSum string
	)
	err := e.store.DB().QueryRowContext(ctx,
		`SELECT version, sha256 FROM config_history WHERE path = ? ORDER BY version DESC LIMIT 1`, path).Scan(&latest, &latestSum)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("confedit: history of %s: %w", path, err)
	}
	if latestSum == sum {
		return latest, nil
	}

	version := latest + 1
	blob := e.blobPath(path, version)
	if err := os.MkdirAll(filepath.Dir(blob), journalDirMode); err != nil {
		return 0, fmt.Errorf("confedit: history of %s: %w", path, err)
	}
	// Blob first, row second: a row without content would be a version that
	// lists but cannot be restored, a blob without a row is only stray bytes.
	if err := writeAtomic(blob, content, journalFileMode, owner{}); err != nil {
		return 0, fmt.Errorf("confedit: history of %s: %w", path, err)
	}
	var stale []int
	err = e.store.WithTx(ctx, func(tx *sql.Tx) error {
		const insert = `INSERT INTO config_history (path, version, created_at, sha256, size, reason) VALUES (?, ?, ?, ?, ?, ?)`
		if _, err := tx.Exec(insert, path, version, e.now().UTC().Format(timeLayout), sum, len(content), reason); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT version FROM config_history WHERE path = ? AND version <= ?`, path, version-HistoryVersions)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var old int
			if err := rows.Scan(&old); err != nil {
				return err
			}
			stale = append(stale, old)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM config_history WHERE path = ? AND version <= ?`, path, version-HistoryVersions)
		return err
	})
	if err != nil {
		_ = os.Remove(blob)
		return 0, fmt.Errorf("confedit: history of %s: %w", path, err)
	}
	for _, old := range stale {
		_ = os.Remove(e.blobPath(path, old))
	}
	return version, nil
}
