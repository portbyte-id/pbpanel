package logs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Kind groups sources in the viewer.
type Kind string

const (
	KindPanel   Kind = "panel"
	KindAudit   Kind = "audit"
	KindNginx   Kind = "nginx"
	KindSite    Kind = "site"
	KindApp     Kind = "app"
	KindCron    Kind = "cron"
	KindDocker  Kind = "docker"
	KindJournal Kind = "journal"
)

var (
	// ErrNotFound is returned for an id no registered source or provider owns.
	ErrNotFound = errors.New("logs: unknown source")
	// ErrUnavailable is returned when a known source cannot be read right now:
	// the file does not exist yet, or no Docker client is wired.
	ErrUnavailable = errors.New("logs: source unavailable")
	// ErrInvalidSource is returned when a Source fails validation.
	ErrInvalidSource = errors.New("logs: invalid source")
	// ErrBadQuery is returned for an empty, oversized or uncompilable query.
	ErrBadQuery = errors.New("logs: invalid query")
)

var (
	idPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	containerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	// unitPattern is a systemd unit name: it is passed to journalctl as one
	// argument, and nothing with a path separator or a shell character is one.
	unitPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@_.:-]{0,127}$`)
)

// Source is one readable log. Exactly one of Path, Container and Unit is set.
// Path, Container and Unit are server-side only and never serialised.
type Source struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Kind      Kind   `json:"kind"`
	Path      string `json:"-"`
	Container string `json:"-"`
	// Unit is a systemd unit whose journal is the log.
	Unit string `json:"-"`
}

func (s Source) validate() error {
	if !idPattern.MatchString(s.ID) {
		return fmt.Errorf("%w: id %q", ErrInvalidSource, s.ID)
	}
	if s.Label == "" || s.Kind == "" {
		return fmt.Errorf("%w: %s needs a label and kind", ErrInvalidSource, s.ID)
	}
	set := 0
	for _, v := range []string{s.Path, s.Container, s.Unit} {
		if v != "" {
			set++
		}
	}
	switch {
	case set > 1:
		return fmt.Errorf("%w: %s sets more than one of path, container and unit", ErrInvalidSource, s.ID)
	case s.Container != "":
		if !containerPattern.MatchString(s.Container) {
			return fmt.Errorf("%w: %s container name", ErrInvalidSource, s.ID)
		}
	case s.Unit != "":
		if !unitPattern.MatchString(s.Unit) {
			return fmt.Errorf("%w: %s unit name", ErrInvalidSource, s.ID)
		}
	case s.Path != "":
		if strings.ContainsRune(s.Path, 0) || (!strings.HasPrefix(s.Path, "/") && !isAbs(s.Path)) {
			return fmt.Errorf("%w: %s path must be absolute", ErrInvalidSource, s.ID)
		}
	default:
		return fmt.Errorf("%w: %s has no path, container or unit", ErrInvalidSource, s.ID)
	}
	return nil
}

// Provider supplies sources that change at runtime (sites, apps, cron jobs,
// containers). The httpapi wiring implements it over the stores it already has.
type Provider interface {
	LogSources(ctx context.Context) ([]Source, error)
}

// ProviderFunc adapts a function to Provider.
type ProviderFunc func(ctx context.Context) ([]Source, error)

// LogSources implements Provider.
func (f ProviderFunc) LogSources(ctx context.Context) ([]Source, error) { return f(ctx) }

// DockerOptions selects what a container log stream returns.
type DockerOptions struct {
	// Tail is the number of trailing lines to start from; 0 means all.
	Tail int
	// Follow keeps the stream open for new lines.
	Follow bool
}

// DockerLogs is the narrow view of the Docker client the viewer needs. The
// returned stream must already be demultiplexed to plain text, one log line per
// newline; closing it must abort a blocked Read.
type DockerLogs interface {
	ContainerLogs(ctx context.Context, name string, opts DockerOptions) (io.ReadCloser, error)
}

// JournalOptions selects what a unit's journal stream returns.
type JournalOptions struct {
	// Tail is the number of trailing lines to start from; 0 means all.
	Tail int
	// Follow keeps the stream open for new lines.
	Follow bool
}

// JournalLogs is the narrow view of journalctl the viewer needs for a unit that
// runs natively. The stream is one line per newline; closing it must abort a
// blocked Read.
type JournalLogs interface {
	JournalLogs(ctx context.Context, unit string, opts JournalOptions) (io.ReadCloser, error)
}

// Registry resolves source ids. It is safe for concurrent use.
type Registry struct {
	docker  DockerLogs
	journal JournalLogs

	mu        sync.RWMutex
	static    []Source
	providers []Provider
}

// NewRegistry returns an empty registry. docker may be nil; Docker sources then
// report ErrUnavailable.
func NewRegistry(docker DockerLogs) *Registry { return &Registry{docker: docker} }

// WithJournal supplies the journal reader that unit sources use. Without it a
// unit source reports ErrUnavailable.
func (r *Registry) WithJournal(j JournalLogs) *Registry {
	r.journal = j
	return r
}

// Add registers a fixed source. A duplicate id is rejected.
func (r *Registry) Add(s Source) error {
	if err := s.validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.static {
		if e.ID == s.ID {
			return fmt.Errorf("%w: duplicate id %s", ErrInvalidSource, s.ID)
		}
	}
	r.static = append(r.static, s)
	return nil
}

// AddProvider registers a dynamic provider.
func (r *Registry) AddProvider(p Provider) {
	r.mu.Lock()
	r.providers = append(r.providers, p)
	r.mu.Unlock()
}

// List returns every valid source, fixed ones first on an id clash, sorted by
// kind then label. A failing provider does not hide the others: the sources
// gathered so far are returned together with the joined provider errors.
func (r *Registry) List(ctx context.Context) ([]Source, error) {
	r.mu.RLock()
	static := append([]Source(nil), r.static...)
	providers := append([]Provider(nil), r.providers...)
	r.mu.RUnlock()

	seen := make(map[string]bool, len(static))
	out := make([]Source, 0, len(static))
	for _, s := range static {
		seen[s.ID] = true
		out = append(out, s)
	}
	var errs []error
	for _, p := range providers {
		got, err := p.LogSources(ctx)
		if err != nil {
			errs = append(errs, err)
		}
		for _, s := range got {
			if seen[s.ID] || s.validate() != nil {
				continue
			}
			seen[s.ID] = true
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Label < out[j].Label
	})
	return out, errors.Join(errs...)
}

// Resolve returns the source with the given id. Anything that is not exactly a
// registered id, including path-shaped input, is ErrNotFound.
func (r *Registry) Resolve(ctx context.Context, id string) (Source, error) {
	if !idPattern.MatchString(id) {
		return Source{}, ErrNotFound
	}
	list, _ := r.List(ctx)
	for _, s := range list {
		if s.ID == id {
			return s, nil
		}
	}
	return Source{}, ErrNotFound
}
