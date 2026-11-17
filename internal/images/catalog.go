package images

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

// Kind names an image family.
type Kind string

// Families.
const (
	KindPHP   Kind = "php"
	KindNginx Kind = "nginx"
)

// State is where a build is.
type State string

// Build states. A build is `ready` once it has passed its validator, whether or
// not it is the one served; `pruned` builds keep their row and lose their image.
const (
	StateBuilding State = "building"
	StateReady    State = "ready"
	StateFailed   State = "failed"
	StatePruned   State = "pruned"
)

// Errors a caller maps to a status.
var (
	// ErrNotFound is a build that does not exist in this family.
	ErrNotFound = errors.New("images: no such build")
	// ErrBusy is a build or swap requested while one is already running.
	ErrBusy = errors.New("images: a build or a swap is already running for this image")
	// ErrNotReady is an activation of a build that did not pass its validator or
	// whose image has been removed.
	ErrNotReady = errors.New("images: the build is not ready to serve")
)

// Build is one image the panel built for a family.
type Build struct {
	ID      string `json:"id"`
	Kind    Kind   `json:"kind"`
	Version string `json:"version"`
	// Tag is the reference the image is stored under.
	Tag string `json:"tag"`
	// ImageID is the content address of the image, empty when it never existed.
	ImageID    string   `json:"image_id"`
	Extensions []string `json:"extensions"`
	State      State    `json:"state"`
	// Output is the end of the build's output, or the reason it failed.
	Output     string     `json:"output"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at"`
	// Current and Previous say which build the family serves and which it
	// served before. They are set by Family.Builds, not stored on the row.
	Current  bool `json:"current"`
	Previous bool `json:"previous"`
}

// Pointers names the builds a family serves: the current one and the one before.
type Pointers struct {
	Current  string
	Previous string
}

// Catalog is the database side of the builds.
type Catalog struct {
	st *store.Store
}

// NewCatalog returns the catalogue over a store that has migration 0015.
func NewCatalog(st *store.Store) *Catalog { return &Catalog{st: st} }

func (c *Catalog) insert(ctx context.Context, b Build) error {
	exts, err := json.Marshal(nonNil(b.Extensions))
	if err != nil {
		return err
	}
	_, err = c.st.DB().ExecContext(ctx, `
		INSERT INTO image_builds (id, kind, version, tag, image_id, extensions, state, output, created_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '')`,
		b.ID, string(b.Kind), b.Version, b.Tag, b.ImageID, string(exts), string(b.State), b.Output,
		b.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("images: record build %s: %w", b.ID, err)
	}
	return nil
}

// finish records a build's outcome and when it happened.
func (c *Catalog) finish(ctx context.Context, id string, state State, imageID, output string, at time.Time) error {
	_, err := c.st.DB().ExecContext(ctx,
		`UPDATE image_builds SET state = ?, image_id = ?, output = ?, finished_at = ? WHERE id = ?`,
		string(state), imageID, output, at.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("images: finish build %s: %w", id, err)
	}
	return nil
}

// Get returns one build by ID.
func (c *Catalog) Get(ctx context.Context, id string) (Build, error) {
	rows, err := c.query(ctx, `WHERE id = ?`, id)
	if err != nil {
		return Build{}, err
	}
	if len(rows) == 0 {
		return Build{}, ErrNotFound
	}
	return rows[0], nil
}

// list returns a family's builds, newest first.
func (c *Catalog) list(ctx context.Context, kind Kind, version string) ([]Build, error) {
	return c.query(ctx, `WHERE kind = ? AND version = ?`, string(kind), version)
}

// interrupted returns builds that were still running when the daemon stopped.
func (c *Catalog) interrupted(ctx context.Context, kind Kind, version string) ([]Build, error) {
	return c.query(ctx, `WHERE kind = ? AND version = ? AND state = 'building'`, string(kind), version)
}

func (c *Catalog) query(ctx context.Context, where string, args ...any) ([]Build, error) {
	rows, err := c.st.DB().QueryContext(ctx, `
		SELECT id, kind, version, tag, image_id, extensions, state, output, created_at, finished_at
		FROM image_builds `+where+` ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("images: list builds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Build
	for rows.Next() {
		var (
			b                 Build
			kind, state, exts string
			created, finished string
		)
		if err := rows.Scan(&b.ID, &kind, &b.Version, &b.Tag, &b.ImageID, &exts, &state, &b.Output, &created, &finished); err != nil {
			return nil, fmt.Errorf("images: list builds: %w", err)
		}
		b.Kind, b.State = Kind(kind), State(state)
		if err := json.Unmarshal([]byte(exts), &b.Extensions); err != nil {
			return nil, fmt.Errorf("images: build %s: stored extensions are damaged: %w", b.ID, err)
		}
		b.Extensions = nonNil(b.Extensions)
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			b.CreatedAt = t
		}
		if t, err := time.Parse(time.RFC3339Nano, finished); err == nil {
			b.FinishedAt = &t
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Pointers returns the builds a family serves. Both are empty before the first build.
func (c *Catalog) Pointers(ctx context.Context, kind Kind, version string) (Pointers, error) {
	var p Pointers
	err := c.st.DB().QueryRowContext(ctx,
		`SELECT current, previous FROM image_pointers WHERE kind = ? AND version = ?`,
		string(kind), version).Scan(&p.Current, &p.Previous)
	if errors.Is(err, sql.ErrNoRows) {
		return Pointers{}, nil
	}
	if err != nil {
		return Pointers{}, fmt.Errorf("images: read pointers: %w", err)
	}
	return p, nil
}

func (c *Catalog) setPointers(ctx context.Context, kind Kind, version string, p Pointers) error {
	_, err := c.st.DB().ExecContext(ctx, `
		INSERT INTO image_pointers (kind, version, current, previous) VALUES (?, ?, ?, ?)
		ON CONFLICT(kind, version) DO UPDATE SET current = excluded.current, previous = excluded.previous`,
		string(kind), version, p.Current, p.Previous)
	if err != nil {
		return fmt.Errorf("images: write pointers: %w", err)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
