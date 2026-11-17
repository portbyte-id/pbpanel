package apps

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/store"
)

// Applier is the config editor, narrowed to the one method the manager calls.
// *confedit.Editor implements it; tests substitute a recorder.
type Applier interface {
	Apply(ctx context.Context, ch confedit.Change) (confedit.Result, error)
}

// NodeResolver finds the node binary of an installed Node.js version. The
// runtimes manager implements it.
type NodeResolver interface {
	NodePath(version string) (string, error)
}

// Options configures a Manager.
type Options struct {
	Store      *store.Store
	Applier    Applier
	Supervisor Supervisor
	Paths      Paths
	// Node resolves node versions for Node apps. Nil refuses Node apps.
	Node NodeResolver
	// PrepareDirs creates the app's working directory and gives it to the app's
	// account. Nil does nothing, which is what the tests want: ownership is a
	// property of a real host.
	PrepareDirs func(App) error
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Manager owns the apps: their rows, their program files and their processes.
type Manager struct {
	db   *sql.DB
	ed   Applier
	sv   Supervisor
	rel  *reloader
	p    Paths
	node NodeResolver
	prep func(App) error
	now  func() time.Time
}

// New returns a Manager.
func New(o Options) (*Manager, error) {
	if o.Store == nil || o.Applier == nil || o.Supervisor == nil {
		return nil, errors.New("apps: Store, Applier and Supervisor are required")
	}
	m := &Manager{db: o.Store.DB(), ed: o.Applier, sv: o.Supervisor, p: o.Paths, node: o.Node, prep: o.PrepareDirs, now: o.Now}
	if m.now == nil {
		m.now = time.Now
	}
	m.rel = &reloader{sv: o.Supervisor, autostart: m.autostart}
	return m, nil
}

// Paths returns the manager's path set.
func (m *Manager) Paths() Paths { return m.p }

// Status is an app and what supervisord says about it.
type Status struct {
	App
	// State is supervisord's state name, or "unknown" when supervisord could not
	// be asked, or "not loaded" when it has no such program.
	State      string `json:"state"`
	PID        int    `json:"pid"`
	UptimeSecs int64  `json:"uptime_seconds"`
	ExitStatus int    `json:"exitstatus"`
	SpawnError string `json:"spawn_error"`
	ProxyURL   string `json:"proxy_url"`
}

func (m *Manager) status(a App, procs map[string]Process, known bool) Status {
	s := Status{App: a, ProxyURL: a.ProxyURL(), State: "unknown"}
	if !known {
		return s
	}
	p, ok := procs[a.Name]
	if !ok {
		s.State = "not loaded"
		return s
	}
	s.State, s.PID, s.ExitStatus, s.SpawnError = p.State, p.PID, p.ExitStatus, p.SpawnErr
	if p.Running() && p.PID > 0 && p.Now >= p.Start && p.Start > 0 {
		s.UptimeSecs = p.Now - p.Start
	}
	return s
}

func (m *Manager) processes(ctx context.Context) (map[string]Process, bool) {
	list, err := m.sv.Processes(ctx)
	if err != nil {
		return nil, false
	}
	out := make(map[string]Process, len(list))
	for _, p := range list {
		out[p.Name] = p
	}
	return out, true
}

// List returns every app with its live state. An unreachable supervisord is not
// an error here: the apps are the panel's own data, and "unknown" says why the
// state is missing.
func (m *Manager) List(ctx context.Context) ([]Status, error) {
	rows, err := m.db.QueryContext(ctx, selectApp+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var all []App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	procs, known := m.processes(ctx)
	out := make([]Status, 0, len(all))
	for _, a := range all {
		out = append(out, m.status(a, procs, known))
	}
	return out, nil
}

// Get returns one app with its live state.
func (m *Manager) Get(ctx context.Context, name string) (Status, error) {
	a, err := m.load(ctx, name)
	if err != nil {
		return Status{}, err
	}
	procs, known := m.processes(ctx)
	return m.status(a, procs, known), nil
}

const selectApp = `SELECT id, name, type, command, args, workdir, env, run_user, node_version, autostart, restart, port, created_at FROM apps`

type scanner interface{ Scan(dest ...any) error }

func scanApp(r scanner) (App, error) {
	var (
		a         App
		args, env string
		autostart int
		port      sql.NullInt64
	)
	if err := r.Scan(&a.ID, &a.Name, &a.Type, &a.Command, &args, &a.WorkDir, &env, &a.User,
		&a.NodeVersion, &autostart, &a.Restart, &port, &a.CreatedAt); err != nil {
		return a, err
	}
	if err := json.Unmarshal([]byte(args), &a.Args); err != nil {
		return a, fmt.Errorf("apps: stored args of %s: %w", a.Name, err)
	}
	if err := json.Unmarshal([]byte(env), &a.Env); err != nil {
		return a, fmt.Errorf("apps: stored env of %s: %w", a.Name, err)
	}
	if a.Args == nil {
		a.Args = []string{}
	}
	if a.Env == nil {
		a.Env = map[string]string{}
	}
	a.Autostart = autostart != 0
	if port.Valid {
		p := int(port.Int64)
		a.Port = &p
	}
	return a, nil
}

func (m *Manager) load(ctx context.Context, name string) (App, error) {
	a, err := scanApp(m.db.QueryRowContext(ctx, selectApp+` WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return App{}, ErrNotFound
	}
	return a, err
}

// autostart is the reloader's question: does the app by this name start when it
// is first loaded? A program the database does not know (a file the operator
// added by hand) does not.
func (m *Manager) autostart(ctx context.Context, name string) bool {
	var v int
	if err := m.db.QueryRowContext(ctx, `SELECT autostart FROM apps WHERE name = ?`, name).Scan(&v); err != nil {
		return false
	}
	return v != 0
}

// render builds the program file of an app, resolving the node binary of a Node
// app. A version that is not installed is refused here, before anything is
// written, so an app is never saved against a runtime it cannot run on.
func (m *Manager) render(a App) ([]byte, error) {
	nodeBin := ""
	if a.Type == TypeNode {
		if m.node == nil {
			return nil, fmt.Errorf("%w: Node.js runtimes are not available on this host", ErrNodeMissing)
		}
		bin, err := m.node.NodePath(a.NodeVersion)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNodeMissing, err)
		}
		nodeBin = bin
	}
	content := Render(a, m.p, nodeBin)
	if err := CheckProgramFile(content); err != nil {
		return nil, fmt.Errorf("apps: the rendered program file is not valid: %w", err)
	}
	return content, nil
}

func (m *Manager) change(a App, content []byte, del bool, reason string) confedit.Change {
	f := confedit.File{Path: m.p.ConfFile(a.Name), Content: content, Delete: del}
	return confedit.Change{Files: []confedit.File{f}, Services: []confedit.Service{m.rel}, Reason: reason}
}

// Create saves an app and starts it when autostart is set. The warnings are
// problems that did not stop the save, such as a program that would not stay up.
func (m *Manager) Create(ctx context.Context, in Input) (Status, []string, error) {
	a, err := in.Normalize(m.p.DefaultWorkDir)
	if err != nil {
		return Status{}, nil, err
	}
	if _, err := m.load(ctx, a.Name); err == nil {
		return Status{}, nil, ErrExists
	} else if !errors.Is(err, ErrNotFound) {
		return Status{}, nil, err
	}
	content, err := m.render(a)
	if err != nil {
		return Status{}, nil, err
	}
	if err := m.prepareDirs(a); err != nil {
		return Status{}, nil, err
	}
	if err := m.insert(ctx, &a); err != nil {
		return Status{}, nil, err
	}
	res, err := m.ed.Apply(ctx, m.change(a, content, false, "create app "+a.Name))
	if err != nil {
		// The row was written first so the reloader can read the app's autostart
		// flag; a failed apply puts the files back, and the row goes with them.
		_, _ = m.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM apps WHERE name = ?`, a.Name)
		return Status{}, nil, err
	}
	st, _ := m.Get(ctx, a.Name)
	return st, append(res.Warnings, m.rel.takeWarnings()...), nil
}

func (m *Manager) prepareDirs(a App) error {
	if err := os.MkdirAll(m.p.LogDir(a.Name), 0o755); err != nil {
		return fmt.Errorf("apps: create the log directory: %w", err)
	}
	if m.prep != nil {
		if err := m.prep(a); err != nil {
			return fmt.Errorf("apps: prepare %s: %w", a.WorkDir, err)
		}
	}
	return nil
}

func (m *Manager) insert(ctx context.Context, a *App) error {
	args, _ := json.Marshal(a.Args)
	env, _ := json.Marshal(a.Env)
	a.CreatedAt = m.now().UTC().Format(time.RFC3339)
	var port any
	if a.Port != nil {
		port = *a.Port
	}
	res, err := m.db.ExecContext(ctx,
		`INSERT INTO apps (name, type, command, args, workdir, env, run_user, node_version, autostart, restart, port, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Name, string(a.Type), a.Command, string(args), a.WorkDir, string(env), a.User, a.NodeVersion,
		boolInt(a.Autostart), string(a.Restart), port, a.CreatedAt)
	if err != nil {
		return portErr(err)
	}
	a.ID, _ = res.LastInsertId()
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// portErr turns the port index's violation into the sentinel.
func portErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "apps.port") {
		return ErrPortInUse
	}
	return err
}

// Update replaces an app's definition. The name cannot change: it is the program
// name supervisord knows and the name of the log directory.
func (m *Manager) Update(ctx context.Context, name string, in Input) (Status, []string, error) {
	old, err := m.load(ctx, name)
	if err != nil {
		return Status{}, nil, err
	}
	if in.Name != "" && in.Name != name {
		return Status{}, nil, invalid("name", "an app cannot be renamed; create a new one")
	}
	in.Name = name
	a, err := in.Normalize(m.p.DefaultWorkDir)
	if err != nil {
		return Status{}, nil, err
	}
	content, err := m.render(a)
	if err != nil {
		return Status{}, nil, err
	}
	if err := m.prepareDirs(a); err != nil {
		return Status{}, nil, err
	}
	a.ID, a.CreatedAt = old.ID, old.CreatedAt
	if err := m.write(ctx, a); err != nil {
		return Status{}, nil, err
	}
	res, err := m.ed.Apply(ctx, m.change(a, content, false, "edit app "+a.Name))
	if err != nil {
		_ = m.write(context.WithoutCancel(ctx), old)
		return Status{}, nil, err
	}
	st, _ := m.Get(ctx, name)
	return st, append(res.Warnings, m.rel.takeWarnings()...), nil
}

func (m *Manager) write(ctx context.Context, a App) error {
	args, _ := json.Marshal(a.Args)
	env, _ := json.Marshal(a.Env)
	var port any
	if a.Port != nil {
		port = *a.Port
	}
	_, err := m.db.ExecContext(ctx,
		`UPDATE apps SET type=?, command=?, args=?, workdir=?, env=?, run_user=?, node_version=?, autostart=?, restart=?, port=? WHERE name=?`,
		string(a.Type), a.Command, string(args), a.WorkDir, string(env), a.User, a.NodeVersion,
		boolInt(a.Autostart), string(a.Restart), port, a.Name)
	return portErr(err)
}

// Delete stops an app and removes its definition and program file. Its working
// directory and its logs are left alone: they hold the operator's files.
func (m *Manager) Delete(ctx context.Context, name string) error {
	a, err := m.load(ctx, name)
	if err != nil {
		return err
	}
	if _, err := m.ed.Apply(ctx, m.change(a, nil, true, "delete app "+name)); err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx, `DELETE FROM apps WHERE name = ?`, name)
	return err
}

// Start starts an app.
func (m *Manager) Start(ctx context.Context, name string) error {
	if _, err := m.load(ctx, name); err != nil {
		return err
	}
	return m.sv.Start(ctx, name)
}

// Stop stops an app. Stopping one that is not running is not an error.
func (m *Manager) Stop(ctx context.Context, name string) error {
	if _, err := m.load(ctx, name); err != nil {
		return err
	}
	if err := m.sv.Stop(ctx, name); err != nil && !errors.Is(err, ErrNotRunning) {
		return err
	}
	return nil
}

// Restart stops and starts an app. An app that was stopped is started.
func (m *Manager) Restart(ctx context.Context, name string) error {
	if err := m.Stop(ctx, name); err != nil {
		return err
	}
	return m.sv.Start(ctx, name)
}

// UsingNode lists the apps that run on a Node.js version, so the runtimes page
// can refuse to remove one that is in use. version is a full x.y.z.
func (m *Manager) UsingNode(ctx context.Context, resolve func(string) (string, bool)) (map[string][]string, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT name, node_version FROM apps WHERE type = 'node'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var name, want string
		if err := rows.Scan(&name, &want); err != nil {
			return nil, err
		}
		if full, ok := resolve(want); ok {
			out[full] = append(out[full], name)
		}
	}
	return out, rows.Err()
}

// LogChunk is a slice of an app's log.
type LogChunk struct {
	Data string `json:"data"`
	// Offset is where to continue reading: pass it back as the next offset.
	Offset int64 `json:"offset"`
}

// MaxLogChunk bounds one read of a log.
const MaxLogChunk = 256 << 10

// Log reads an app's log file. A negative offset, or one past the end of the
// file (it was rotated), reads the last max bytes; otherwise it reads on from
// offset. A log that does not exist yet is empty, not an error: the app may not
// have started.
func (m *Manager) Log(ctx context.Context, name string, offset int64, max int) (LogChunk, error) {
	if _, err := m.load(ctx, name); err != nil {
		return LogChunk{}, err
	}
	if max <= 0 || max > MaxLogChunk {
		max = MaxLogChunk
	}
	f, err := os.Open(m.p.LogFile(name))
	if errors.Is(err, os.ErrNotExist) {
		return LogChunk{}, nil
	}
	if err != nil {
		return LogChunk{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return LogChunk{}, err
	}
	size := info.Size()
	start := offset
	if start < 0 || start > size {
		start = size - int64(max)
		if start < 0 {
			start = 0
		}
	}
	if size-start > int64(max) {
		start = size - int64(max)
	}
	buf := make([]byte, size-start)
	n, err := f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return LogChunk{}, err
	}
	return LogChunk{Data: string(buf[:n]), Offset: start + int64(n)}, nil
}
