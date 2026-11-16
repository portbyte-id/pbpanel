package settings

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Type is a setting's value type. Values are stored as text and parsed by type,
// so the type decides both how the Settings page renders a key and what a
// written value must look like.
type Type string

const (
	TypeInt      Type = "int"
	TypeString   Type = "string"
	TypeBool     Type = "bool"
	TypeDuration Type = "duration"
	TypePort     Type = "port"
	TypePath     Type = "path"
	TypeEnum     Type = "enum"
)

// ErrNotFound is what errors.Is matches for a key that is not registered.
var ErrNotFound = errors.New("settings: unknown key")

// NotFoundError names an unregistered key. Get and Set return it instead of
// creating a row, so a typo in `panel set` cannot leave a dead setting behind.
type NotFoundError struct {
	Name string
}

func (e *NotFoundError) Error() string {
	return "settings: unknown key " + strconv.Quote(e.Name)
}

func (e *NotFoundError) Is(target error) bool {
	return target == ErrNotFound
}

// ValidationError reports a value the key's type or validator rejected. Reason
// is the validator's own message, shown to the operator as-is.
type ValidationError struct {
	Name   string
	Value  string
	Reason string
}

func (e *ValidationError) Error() string {
	return e.Reason
}

// Meta is what the Settings page needs to render one key.
type Meta struct {
	Name            string
	Type            Type
	Default         string
	RestartRequired bool
	Description     string
	Group           string
	Allowed         []string // enum keys only; nil for every other type
}

// Group is an ordered block of keys. Groups and keys both keep registry order.
type Group struct {
	Name string
	Keys []Meta
}

// Setting is a key's metadata plus the value in effect. IsDefault reports that
// no stored row exists, so Value is the registry default.
type Setting struct {
	Meta
	Value     string
	IsDefault bool
}

// Change describes one successful write, in the order subscribers see it.
type Change struct {
	Name string
	Old  string
	New  string
}

// Subscriber is called once per successful write, after the value is stored.
// It runs synchronously on the writing goroutine, so it must not block for long
// and must not call Set on the same registry (that would notify again).
type Subscriber func(Change)

// Store is the persistence the registry needs; *store.Store implements it.
// The registry is the only thing that parses or validates values, so the store
// sees text and nothing else.
type Store interface {
	// LoadSetting returns the stored text for name. ok is false when the
	// operator has never changed it.
	LoadSetting(ctx context.Context, name string) (value string, ok bool, err error)
	// LoadSettings returns every stored setting keyed by name.
	LoadSettings(ctx context.Context) (map[string]string, error)
	// SaveSetting stores one value, replacing any previous one.
	SaveSetting(ctx context.Context, name, value string) error
}

// key is a Meta plus the per-key validator that runs after the type check.
type key struct {
	meta  Meta
	check func(string) error
}

type subscription struct {
	id uint64
	fn Subscriber
}

// Registry holds the key table and the store it reads from. It is safe for
// concurrent use.
type Registry struct {
	keys   []key
	byName map[string]int
	store  Store

	mu     sync.Mutex
	subs   []subscription
	nextID uint64
}

// New builds the registry for the panel's keys. It fails if any key's default
// does not satisfy that key's own validator, so a bad table cannot start the
// daemon.
func New(st Store) (*Registry, error) {
	return newRegistry(st, defaultKeys())
}

func newRegistry(st Store, keys []key) (*Registry, error) {
	if st == nil {
		return nil, errors.New("settings: nil store")
	}
	byName, err := indexKeys(keys)
	if err != nil {
		return nil, err
	}
	return &Registry{keys: keys, byName: byName, store: st}, nil
}

// indexKeys runs the boot-time key table checks and maps each name to its
// position. Both New and Catalog go through it, so the path that only describes
// keys cannot accept a table New would reject.
func indexKeys(keys []key) (map[string]int, error) {
	byName := make(map[string]int, len(keys))
	for i, k := range keys {
		if err := checkKey(k); err != nil {
			return nil, err
		}
		if _, dup := byName[k.meta.Name]; dup {
			return nil, fmt.Errorf("settings: key %s is registered twice", k.meta.Name)
		}
		byName[k.meta.Name] = i
	}
	return byName, nil
}

// Catalog returns the key table as groups, without a store and without reading
// any value. It is for a caller that only needs to describe the keys: `panel set
// --help` lists what it accepts, and the registry is the only place that list
// lives.
func Catalog() ([]Group, error) {
	keys := defaultKeys()
	if _, err := indexKeys(keys); err != nil {
		return nil, err
	}
	return groupsOf(keys), nil
}

// Get returns one key's metadata and the value in effect. A stored value that
// no longer validates is an error rather than a silent fallback: `panel set`
// repairs it, and Set does not consult the old value.
func (r *Registry) Get(ctx context.Context, name string) (Setting, error) {
	k, err := r.lookup(name)
	if err != nil {
		return Setting{}, err
	}

	raw, ok, err := r.store.LoadSetting(ctx, name)
	if err != nil {
		return Setting{}, fmt.Errorf("settings: read %s: %w", name, err)
	}
	if !ok {
		return Setting{Meta: metaCopy(k.meta), Value: k.meta.Default, IsDefault: true}, nil
	}
	if err := validate(k, raw); err != nil {
		return Setting{}, fmt.Errorf("settings: stored value for %s is invalid: %w", name, err)
	}
	return Setting{Meta: metaCopy(k.meta), Value: raw}, nil
}

// Validate reports whether value is acceptable for name without storing it. It
// is what lets a caller with a side effect (the listener rebind for panel.port)
// check the value first and only then act, instead of acting on a value Set
// would have refused.
func (r *Registry) Validate(name, value string) error {
	k, err := r.lookup(name)
	if err != nil {
		return err
	}
	return validate(k, value)
}

// Set validates, stores, then notifies every subscriber once. A rejected value
// changes nothing and returns the validator's message. Writing a value that is
// already in effect still counts as a successful write and still notifies.
func (r *Registry) Set(ctx context.Context, name, value string) error {
	k, err := r.lookup(name)
	if err != nil {
		return err
	}
	if err := validate(k, value); err != nil {
		return err
	}

	// The old value is read without validating it: a corrupt row must stay
	// repairable through this path.
	old, ok, err := r.store.LoadSetting(ctx, name)
	if err != nil {
		return fmt.Errorf("settings: read %s: %w", name, err)
	}
	if !ok {
		old = k.meta.Default
	}

	if err := r.store.SaveSetting(ctx, name, value); err != nil {
		return fmt.Errorf("settings: write %s: %w", name, err)
	}
	r.notify(Change{Name: name, Old: old, New: value})
	return nil
}

// Int returns an int setting. Port settings are ints too.
func (r *Registry) Int(ctx context.Context, name string) (int, error) {
	s, err := r.Get(ctx, name)
	if err != nil {
		return 0, err
	}
	switch s.Type {
	case TypeInt, TypePort:
		return strconv.Atoi(s.Value)
	default:
		return 0, typeMismatch(name, s.Type, TypeInt)
	}
}

// Str returns a text setting: string, path or enum.
func (r *Registry) Str(ctx context.Context, name string) (string, error) {
	s, err := r.Get(ctx, name)
	if err != nil {
		return "", err
	}
	switch s.Type {
	case TypeString, TypePath, TypeEnum:
		return s.Value, nil
	default:
		return "", typeMismatch(name, s.Type, TypeString)
	}
}

// Bool returns a boolean setting.
func (r *Registry) Bool(ctx context.Context, name string) (bool, error) {
	s, err := r.Get(ctx, name)
	if err != nil {
		return false, err
	}
	if s.Type != TypeBool {
		return false, typeMismatch(name, s.Type, TypeBool)
	}
	return strconv.ParseBool(s.Value)
}

// Duration returns a duration setting.
func (r *Registry) Duration(ctx context.Context, name string) (time.Duration, error) {
	s, err := r.Get(ctx, name)
	if err != nil {
		return 0, err
	}
	if s.Type != TypeDuration {
		return 0, typeMismatch(name, s.Type, TypeDuration)
	}
	return time.ParseDuration(s.Value)
}

// Snapshot returns every key in registry order with its value in effect, for
// the Settings page in one read.
func (r *Registry) Snapshot(ctx context.Context) ([]Setting, error) {
	stored, err := r.store.LoadSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("settings: read all: %w", err)
	}

	out := make([]Setting, 0, len(r.keys))
	for _, k := range r.keys {
		raw, ok := stored[k.meta.Name]
		if !ok {
			out = append(out, Setting{Meta: metaCopy(k.meta), Value: k.meta.Default, IsDefault: true})
			continue
		}
		if err := validate(k, raw); err != nil {
			return nil, fmt.Errorf("settings: stored value for %s is invalid: %w", k.meta.Name, err)
		}
		out = append(out, Setting{Meta: metaCopy(k.meta), Value: raw})
	}
	return out, nil
}

// Groups returns the registry as an ordered, grouped list of metadata. Groups
// appear in the order their first key is registered.
func (r *Registry) Groups() []Group {
	return groupsOf(r.keys)
}

func groupsOf(keys []key) []Group {
	out := make([]Group, 0, len(keys))
	index := make(map[string]int, len(keys))
	for _, k := range keys {
		i, ok := index[k.meta.Group]
		if !ok {
			i = len(out)
			index[k.meta.Group] = i
			out = append(out, Group{Name: k.meta.Group})
		}
		out[i].Keys = append(out[i].Keys, metaCopy(k.meta))
	}
	return out
}

// Subscribe registers fn and returns an idempotent unsubscribe. Subscribers are
// called in subscription order, after the write is committed.
func (r *Registry) Subscribe(fn Subscriber) (unsubscribe func()) {
	if fn == nil {
		panic("settings: Subscribe with a nil subscriber")
	}

	r.mu.Lock()
	r.nextID++
	id := r.nextID
	r.subs = append(r.subs, subscription{id: id, fn: fn})
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.subs = slices.DeleteFunc(r.subs, func(s subscription) bool { return s.id == id })
		})
	}
}

// notify copies the subscriber slice under the lock and calls it outside, so a
// callback may subscribe or unsubscribe without deadlocking.
func (r *Registry) notify(c Change) {
	r.mu.Lock()
	subs := make([]Subscriber, len(r.subs))
	for i, s := range r.subs {
		subs[i] = s.fn
	}
	r.mu.Unlock()

	for _, fn := range subs {
		fn(c)
	}
}

func (r *Registry) lookup(name string) (key, error) {
	i, ok := r.byName[name]
	if !ok {
		return key{}, &NotFoundError{Name: name}
	}
	return r.keys[i], nil
}

func typeMismatch(name string, have, want Type) error {
	return fmt.Errorf("settings: %s is a %s setting, not a %s", name, have, want)
}

func metaCopy(m Meta) Meta {
	if m.Allowed != nil {
		m.Allowed = slices.Clone(m.Allowed)
	}
	return m
}
