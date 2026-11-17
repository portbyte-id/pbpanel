package firewall

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

// ConfirmWindow is how long the operator has to keep an applied ruleset before the
// previous one returns.
const ConfirmWindow = 60 * time.Second

// Errors returned by the apply manager.
var (
	ErrPending   = errors.New("firewall: an apply is already waiting for confirmation")
	ErrNoPending = errors.New("firewall: no apply is waiting for confirmation")
	ErrExpired   = errors.New("firewall: the confirmation window has passed; the previous rules were restored")
	ErrToken     = errors.New("firewall: confirmation token does not match the pending apply")
)

// Cmd runs iptables tooling. Every call takes an argument vector, never a shell line.
type Cmd interface {
	Outputter
	// RunStdin runs a command with stdin and returns an error carrying its stderr.
	RunStdin(ctx context.Context, stdin string, name string, args ...string) error
}

// ExecCmd is the production Cmd.
type ExecCmd struct{}

// Output implements Outputter.
func (ExecCmd) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	c := exec.CommandContext(ctx, name, args...)
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return out, fmt.Errorf("firewall: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// RunStdin implements Cmd.
func (ExecCmd) RunStdin(ctx context.Context, stdin string, name string, args ...string) error {
	var stderr bytes.Buffer
	c := exec.CommandContext(ctx, name, args...)
	c.Stdin = strings.NewReader(stdin)
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("firewall: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Options configures a Manager.
type Options struct {
	Store *store.Store
	Cmd   Cmd
	// IPv6 includes ip6tables in every apply and rollback.
	IPv6 bool
	// Window overrides ConfirmWindow.
	Window time.Duration
	// Now and After exist for tests. After schedules f and returns a stop function.
	Now     func() time.Time
	After   func(d time.Duration, f func()) (stop func() bool)
	OnError func(error)
}

// Pending describes an apply waiting for confirmation.
type Pending struct {
	Token     string    `json:"token"`
	StartedAt time.Time `json:"startedAt"`
	Deadline  time.Time `json:"deadline"`
}

// Manager applies rulesets with a confirm-or-rollback timer. Only one apply can be
// pending at a time.
type Manager struct {
	st      *store.Store
	cmd     Cmd
	ipv6    bool
	window  time.Duration
	now     func() time.Time
	after   func(time.Duration, func()) func() bool
	onError func(error)

	mu   sync.Mutex
	stop func() bool
}

// NewManager builds a Manager. Call Recover once at startup.
func NewManager(o Options) (*Manager, error) {
	if o.Store == nil || o.Cmd == nil {
		return nil, errors.New("firewall: Options.Store and Options.Cmd are required")
	}
	m := &Manager{st: o.Store, cmd: o.Cmd, ipv6: o.IPv6, window: o.Window, now: o.Now, after: o.After, onError: o.OnError}
	if m.window <= 0 {
		m.window = ConfirmWindow
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.after == nil {
		m.after = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	if m.onError == nil {
		m.onError = func(error) {}
	}
	return m, nil
}

type famCmds struct {
	f       family
	bin     string
	save    string
	restore string
}

func (m *Manager) families() []famCmds {
	fs := []famCmds{{fam4, "iptables", "iptables-save", "iptables-restore"}}
	if m.ipv6 {
		fs = append(fs, famCmds{fam6, "ip6tables", "ip6tables-save", "ip6tables-restore"})
	}
	return fs
}

// snapshot is the owned-chain state of one family, enough to put it back.
type snapshot struct {
	Existed bool   `json:"existed"`
	Hooked  bool   `json:"hooked"`
	Lines   string `json:"lines"` // the -A lines of the owned chains, as iptables-save printed them
}

func (m *Manager) snapshot(ctx context.Context, fc famCmds) (snapshot, error) {
	out, err := m.cmd.Output(ctx, fc.save, "-t", "filter")
	if err != nil {
		return snapshot{}, err
	}
	var s snapshot
	var keep strings.Builder
	for l := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		switch {
		case f[0] == ":"+ChainInput || f[0] == ":"+ChainBlock:
			s.Existed = true
		case f[0] == "-A" && (f[1] == ChainInput || f[1] == ChainBlock):
			keep.WriteString(l + "\n")
		}
	}
	s.Lines = keep.String()
	s.Hooked = m.hooked(ctx, fc)
	return s, nil
}

func (m *Manager) hooked(ctx context.Context, fc famCmds) bool {
	return m.cmd.RunStdin(ctx, "", fc.bin, "-C", "INPUT", "-j", ChainInput) == nil
}

func restoreText(s snapshot) string {
	var b strings.Builder
	b.WriteString("*filter\n:" + ChainInput + " - [0:0]\n:" + ChainBlock + " - [0:0]\n")
	b.WriteString("-F " + ChainInput + "\n-F " + ChainBlock + "\n")
	if s.Existed {
		b.WriteString(s.Lines)
	}
	b.WriteString("COMMIT\n")
	return b.String()
}

// rollbackFamily returns one family's owned chains to a snapshot. When the chains did
// not exist, or were not hooked, it unhooks and leaves them empty.
func (m *Manager) rollbackFamily(ctx context.Context, fc famCmds, s snapshot) error {
	if !s.Hooked {
		for range 8 { // the jump can only be stacked by hand; cap the loop anyway
			if !m.hooked(ctx, fc) {
				break
			}
			if err := m.cmd.RunStdin(ctx, "", fc.bin, "-D", "INPUT", "-j", ChainInput); err != nil {
				return err
			}
		}
	}
	return m.cmd.RunStdin(ctx, restoreText(s), fc.restore, "--noflush")
}

// Apply renders in, applies it, and starts the confirmation timer. The caller sets
// in.Rules, in.Blocklist, in.SSHPorts and in.PanelPort; hook flags are decided here.
// On any failure the previous state is restored before returning.
func (m *Manager) Apply(ctx context.Context, in RenderInput) (Pending, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, err := m.loadPending(ctx); err == nil {
		return Pending{}, ErrPending
	} else if !errors.Is(err, ErrNoPending) {
		return Pending{}, err
	}

	snaps := map[family]snapshot{}
	for _, fc := range m.families() {
		s, err := m.snapshot(ctx, fc)
		if err != nil {
			return Pending{}, err
		}
		snaps[fc.f] = s
	}
	live := in
	live.HookV4 = !snaps[fam4].Hooked
	live.HookV6 = !snaps[fam6].Hooked
	canon := in
	canon.HookV4, canon.HookV6 = true, true // stored text must hook on a clean boot
	liveR, canonR := Render(live), Render(canon)

	texts := map[family]string{fam4: liveR.V4, fam6: liveR.V6}
	for _, fc := range m.families() {
		if err := m.cmd.RunStdin(ctx, texts[fc.f], fc.restore, "--test", "--noflush"); err != nil {
			return Pending{}, fmt.Errorf("firewall: ruleset rejected: %w", err)
		}
	}

	now := m.now().UTC()
	p := Pending{Token: newToken(), StartedAt: now, Deadline: now.Add(m.window)}
	row := pendingRow{Pending: p, newV4: canonR.V4}
	if m.ipv6 {
		row.newV6 = canonR.V6
	}
	row.prevV4 = mustJSON(snaps[fam4])
	if m.ipv6 {
		row.prevV6 = mustJSON(snaps[fam6])
	}
	if err := m.savePending(ctx, row); err != nil {
		return Pending{}, err
	}

	for _, fc := range m.families() {
		if err := m.cmd.RunStdin(ctx, texts[fc.f], fc.restore, "--noflush"); err != nil {
			rbErr := m.rollbackLocked(context.WithoutCancel(ctx), row)
			return Pending{}, errors.Join(fmt.Errorf("firewall: apply %s: %w", fc.bin, err), rbErr)
		}
	}
	m.stop = m.after(m.window, func() { m.expire(p.Token) })
	return p, nil
}

// Confirm keeps the pending ruleset. After the deadline it rolls back instead.
func (m *Manager) Confirm(ctx context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, err := m.loadPending(ctx)
	if err != nil {
		return err
	}
	if row.Token != token {
		return ErrToken
	}
	if !m.now().Before(row.Deadline) {
		if err := m.rollbackLocked(ctx, row); err != nil {
			return err
		}
		return ErrExpired
	}
	err = m.st.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE fw_state SET enabled = 1, confirmed_v4 = ?, confirmed_v6 = ?, confirmed_at = ? WHERE id = 1`,
			row.newV4, row.newV6, m.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM fw_pending WHERE id = 1`)
		return err
	})
	if err != nil {
		return err
	}
	m.stopTimer()
	return nil
}

// Revert rolls the pending apply back now.
func (m *Manager) Revert(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, err := m.loadPending(ctx)
	if err != nil {
		return err
	}
	return m.rollbackLocked(ctx, row)
}

// Recover rolls back an apply left pending by a restart. Call it before serving.
func (m *Manager) Recover(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, err := m.loadPending(ctx)
	if errors.Is(err, ErrNoPending) {
		return nil
	}
	if err != nil {
		return err
	}
	return m.rollbackLocked(ctx, row)
}

// Pending reports the apply awaiting confirmation, or ErrNoPending.
func (m *Manager) Pending(ctx context.Context) (Pending, error) {
	row, err := m.loadPending(ctx)
	return row.Pending, err
}

func (m *Manager) expire(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx := context.Background()
	row, err := m.loadPending(ctx)
	if err != nil || row.Token != token {
		return
	}
	if err := m.rollbackLocked(ctx, row); err != nil {
		m.onError(fmt.Errorf("firewall: rollback after timeout failed: %w", err))
	}
}

// rollbackLocked restores the snapshots in row and clears it. A failure keeps the row,
// so Recover retries on the next start.
func (m *Manager) rollbackLocked(ctx context.Context, row pendingRow) error {
	var errs []error
	for _, fc := range m.families() {
		raw := row.prevV4
		if fc.f == fam6 {
			raw = row.prevV6
		}
		var s snapshot
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			errs = append(errs, fmt.Errorf("firewall: pending snapshot for %s unreadable: %w", fc.bin, err))
			continue
		}
		if err := m.rollbackFamily(ctx, fc, s); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	m.stopTimer()
	_, err := m.st.DB().ExecContext(ctx, `DELETE FROM fw_pending WHERE id = 1`)
	return err
}

func (m *Manager) stopTimer() {
	if m.stop != nil {
		m.stop()
		m.stop = nil
	}
}

type pendingRow struct {
	Pending
	prevV4, prevV6 string
	newV4, newV6   string
}

func (m *Manager) loadPending(ctx context.Context) (pendingRow, error) {
	var r pendingRow
	var started, deadline string
	err := m.st.DB().QueryRowContext(ctx,
		`SELECT token, prev_v4, prev_v6, new_v4, new_v6, started_at, deadline FROM fw_pending WHERE id = 1`).
		Scan(&r.Token, &r.prevV4, &r.prevV6, &r.newV4, &r.newV6, &started, &deadline)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNoPending
	}
	if err != nil {
		return r, err
	}
	if r.StartedAt, err = time.Parse(time.RFC3339Nano, started); err != nil {
		return r, err
	}
	r.Deadline, err = time.Parse(time.RFC3339Nano, deadline)
	return r, err
}

func (m *Manager) savePending(ctx context.Context, r pendingRow) error {
	_, err := m.st.DB().ExecContext(ctx,
		`INSERT INTO fw_pending (id, token, prev_v4, prev_v6, new_v4, new_v6, started_at, deadline)
		 VALUES (1, ?, ?, ?, ?, ?, ?, ?)`,
		r.Token, r.prevV4, r.prevV6, r.newV4, r.newV6,
		r.StartedAt.Format(time.RFC3339Nano), r.Deadline.Format(time.RFC3339Nano))
	return err
}

func newToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return hex.EncodeToString(b[:])
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
