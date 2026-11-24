package firewall

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

// Service errors. Callers match with errors.Is.
var (
	ErrNotEnabled = errors.New("firewall: the firewall is not enabled")
	ErrNotFound   = errors.New("firewall: no such entry")
	ErrDuplicate  = errors.New("firewall: entry already exists")
)

// Masker stops and masks the distribution's own firewall service. osdep.OS
// implements it.
type Masker interface {
	MaskFirewallServices(ctx context.Context) error
}

// ServiceOptions configures a Service.
type ServiceOptions struct {
	Manager *Manager
	Store   *store.Store
	Cmd     Cmd
	Masker  Masker
	// PanelPort returns the port the panel listens on right now.
	PanelPort func() int
	// Root is the filesystem holding /etc/ssh. Nil means the host's.
	Root fs.FS
}

// Service is the firewall as the API sees it: stored rules, built-ins, enable and
// disable, and apply with confirmation.
type Service struct {
	m         *Manager
	st        *store.Store
	cmd       Cmd
	masker    Masker
	panelPort func() int
	root      fs.FS
}

// NewService builds a Service.
func NewService(o ServiceOptions) (*Service, error) {
	if o.Manager == nil || o.Store == nil || o.Cmd == nil || o.PanelPort == nil {
		return nil, errors.New("firewall: ServiceOptions needs Manager, Store, Cmd and PanelPort")
	}
	root := o.Root
	if root == nil {
		root = os.DirFS("/")
	}
	return &Service{m: o.Manager, st: o.Store, cmd: o.Cmd, masker: o.Masker, panelPort: o.PanelPort, root: root}, nil
}

// Builtin is one generated, read-only rule shown in the UI.
type Builtin struct {
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// PublishedPort is a port a non-panel container publishes. Docker inserts its own
// rules ahead of INPUT filtering, so these bypass the panel's firewall.
type PublishedPort struct {
	Container string `json:"container"`
	Port      int    `json:"port"`
	Protocol  string `json:"protocol"`
}

// State is everything the firewall page shows besides the two tables.
type State struct {
	Enabled     bool            `json:"enabled"`
	IPv6        bool            `json:"ipv6"`
	Dirty       bool            `json:"dirty"`
	SSHPorts    []int           `json:"sshPorts"`
	PanelPort   int             `json:"panelPort"`
	Pending     *Pending        `json:"pending"`
	DockerPorts []PublishedPort `json:"dockerPorts"`
	Builtins    []Builtin       `json:"builtins"`
}

// State reads the current state.
func (s *Service) State(ctx context.Context) (State, error) {
	enabled, confirmed, err := s.confirmed(ctx)
	if err != nil {
		return State{}, err
	}
	in, err := s.input(ctx)
	if err != nil {
		return State{}, err
	}
	st := State{
		Enabled: enabled, IPv6: s.m.ipv6, SSHPorts: in.SSHPorts, PanelPort: in.PanelPort,
		DockerPorts: PublishedPorts(ctx, s.cmd), Builtins: builtins(in),
	}
	if st.DockerPorts == nil {
		st.DockerPorts = []PublishedPort{}
	}
	if enabled {
		canon := in
		canon.HookV4, canon.HookV6 = true, true
		r := Render(canon)
		st.Dirty = r.V4 != confirmed[0] || (s.m.ipv6 && r.V6 != confirmed[1])
	}
	if p, err := s.m.Pending(ctx); err == nil {
		st.Pending = &p
	} else if !errors.Is(err, ErrNoPending) {
		return State{}, err
	}
	return st, nil
}

func builtins(in RenderInput) []Builtin {
	ssh := make([]string, len(in.SSHPorts))
	for i, p := range in.SSHPorts {
		ssh[i] = strconv.Itoa(p)
	}
	return []Builtin{
		{"Loopback", "All traffic on the loopback interface"},
		{"Established connections", "Replies to connections the host opened, and related traffic"},
		{"ICMP and ICMPv6", "Echo, unreachable, time exceeded, parameter problem and IPv6 neighbour discovery"},
		{"SSH", "TCP " + strings.Join(ssh, ", ") + " from anywhere"},
		{"Panel", "TCP " + strconv.Itoa(in.PanelPort) + " from anywhere"},
		{"Web", "TCP 80 and TCP/UDP 443 from anywhere"},
	}
}

func (s *Service) confirmed(ctx context.Context) (enabled bool, text [2]string, err error) {
	var en int
	err = s.st.DB().QueryRowContext(ctx, `SELECT enabled, confirmed_v4, confirmed_v6 FROM fw_state WHERE id = 1`).
		Scan(&en, &text[0], &text[1])
	return en == 1, text, err
}

// input assembles the renderer input from the stored rules and the live host.
func (s *Service) input(ctx context.Context) (RenderInput, error) {
	rules, err := s.Rules(ctx)
	if err != nil {
		return RenderInput{}, err
	}
	block, err := s.Blocklist(ctx)
	if err != nil {
		return RenderInput{}, err
	}
	allow, err := s.Allowlist(ctx)
	if err != nil {
		return RenderInput{}, err
	}
	return RenderInput{
		Rules: rules, Blocklist: block, Allowlist: allow,
		SSHPorts:  DetectSSHPorts(ctx, s.cmd, s.root),
		PanelPort: s.panelPort(),
	}, nil
}

// Enable stops the distribution's firewall service, then applies the stored rules
// with the confirmation timer. A first enable that is not confirmed is rolled back
// completely, so the host ends as it began except for the masked service.
func (s *Service) Enable(ctx context.Context) (Pending, error) {
	if _, err := s.m.Pending(ctx); err == nil {
		return Pending{}, ErrPending
	}
	if s.masker != nil {
		if err := s.masker.MaskFirewallServices(ctx); err != nil {
			return Pending{}, err
		}
	}
	return s.apply(ctx)
}

// Apply applies the stored rules to an enabled firewall.
func (s *Service) Apply(ctx context.Context) (Pending, error) {
	enabled, _, err := s.confirmed(ctx)
	if err != nil {
		return Pending{}, err
	}
	if !enabled {
		return Pending{}, ErrNotEnabled
	}
	return s.apply(ctx)
}

func (s *Service) apply(ctx context.Context) (Pending, error) {
	in, err := s.input(ctx)
	if err != nil {
		return Pending{}, err
	}
	return s.m.Apply(ctx, in)
}

// Confirm keeps the pending ruleset.
func (s *Service) Confirm(ctx context.Context, token string) error { return s.m.Confirm(ctx, token) }

// Revert rolls the pending ruleset back now.
func (s *Service) Revert(ctx context.Context) error { return s.m.Revert(ctx) }

// ReconcileDocker re-hooks the Docker filter when the firewall is enabled. Docker may
// have started after the boot restore ran, so DOCKER-USER did not exist then.
func (s *Service) ReconcileDocker(ctx context.Context) error {
	enabled, _, err := s.confirmed(ctx)
	if err != nil || !enabled {
		return err
	}
	return HookDocker(ctx, s.cmd, s.m.ipv6)
}

// Disable unhooks and flushes the panel chains and forgets the confirmed ruleset.
// ufw and firewalld stay masked.
func (s *Service) Disable(ctx context.Context) error { return s.m.Disable(ctx) }

// --- rules -------------------------------------------------------------------

// Rules lists the stored rules in the order they are evaluated.
func (s *Service) Rules(ctx context.Context) ([]Rule, error) {
	rows, err := s.st.DB().QueryContext(ctx,
		`SELECT id, port_start, port_end, protocol, source, comment, enabled FROM fw_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Rule{}
	for rows.Next() {
		var r Rule
		var proto string
		var en int
		if err := rows.Scan(&r.ID, &r.PortStart, &r.PortEnd, &proto, &r.Source, &r.Comment, &en); err != nil {
			return nil, err
		}
		r.Protocol, r.Enabled = Protocol(proto), en == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func normalizeRule(r Rule) (Rule, error) {
	if err := r.Validate(); err != nil {
		return r, err
	}
	if r.Source != "" {
		p, _ := ParsePrefix(r.Source)
		r.Source = p.String()
	}
	return r, nil
}

// AddRule validates and stores a rule. It is not live until the next apply.
func (s *Service) AddRule(ctx context.Context, r Rule) (Rule, error) {
	r, err := normalizeRule(r)
	if err != nil {
		return r, err
	}
	res, err := s.st.DB().ExecContext(ctx,
		`INSERT INTO fw_rules (port_start, port_end, protocol, source, comment, enabled) VALUES (?, ?, ?, ?, ?, ?)`,
		r.PortStart, r.PortEnd, string(r.Protocol), r.Source, r.Comment, b2i(r.Enabled))
	if err != nil {
		return r, err
	}
	r.ID, err = res.LastInsertId()
	return r, err
}

// UpdateRule replaces the rule with the given id.
func (s *Service) UpdateRule(ctx context.Context, id int64, r Rule) (Rule, error) {
	r, err := normalizeRule(r)
	if err != nil {
		return r, err
	}
	res, err := s.st.DB().ExecContext(ctx,
		`UPDATE fw_rules SET port_start=?, port_end=?, protocol=?, source=?, comment=?, enabled=? WHERE id=?`,
		r.PortStart, r.PortEnd, string(r.Protocol), r.Source, r.Comment, b2i(r.Enabled), id)
	if err != nil {
		return r, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return r, ErrNotFound
	}
	r.ID = id
	return r, nil
}

// DeleteRule removes a stored rule.
func (s *Service) DeleteRule(ctx context.Context, id int64) error {
	return s.deleteRow(ctx, `DELETE FROM fw_rules WHERE id = ?`, id)
}

// Blocklist lists the blocked addresses.
func (s *Service) Blocklist(ctx context.Context) ([]BlockEntry, error) {
	rows, err := s.st.DB().QueryContext(ctx, `SELECT id, addr, comment FROM fw_blocklist ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []BlockEntry{}
	for rows.Next() {
		var e BlockEntry
		if err := rows.Scan(&e.ID, &e.Addr, &e.Comment); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddBlock validates and stores a blocklist entry.
func (s *Service) AddBlock(ctx context.Context, e BlockEntry) (BlockEntry, error) {
	if err := e.Validate(); err != nil {
		return e, err
	}
	p, _ := ParsePrefix(e.Addr)
	e.Addr = p.String()
	res, err := s.st.DB().ExecContext(ctx, `INSERT INTO fw_blocklist (addr, comment) VALUES (?, ?)`, e.Addr, e.Comment)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return e, ErrDuplicate
		}
		return e, err
	}
	e.ID, err = res.LastInsertId()
	return e, err
}

// DeleteBlock removes a blocklist entry.
func (s *Service) DeleteBlock(ctx context.Context, id int64) error {
	return s.deleteRow(ctx, `DELETE FROM fw_blocklist WHERE id = ?`, id)
}

// Allowlist lists the always-allowed addresses.
func (s *Service) Allowlist(ctx context.Context) ([]AllowEntry, error) {
	rows, err := s.st.DB().QueryContext(ctx, `SELECT id, addr, comment FROM fw_allowlist ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []AllowEntry{}
	for rows.Next() {
		var e AllowEntry
		if err := rows.Scan(&e.ID, &e.Addr, &e.Comment); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddAllow validates and stores an allow list entry.
func (s *Service) AddAllow(ctx context.Context, e AllowEntry) (AllowEntry, error) {
	if err := e.Validate(); err != nil {
		return e, err
	}
	p, _ := ParsePrefix(e.Addr)
	e.Addr = p.String()
	res, err := s.st.DB().ExecContext(ctx, `INSERT INTO fw_allowlist (addr, comment) VALUES (?, ?)`, e.Addr, e.Comment)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return e, ErrDuplicate
		}
		return e, err
	}
	e.ID, err = res.LastInsertId()
	return e, err
}

// DeleteAllow removes an allow list entry.
func (s *Service) DeleteAllow(ctx context.Context, id int64) error {
	return s.deleteRow(ctx, `DELETE FROM fw_allowlist WHERE id = ?`, id)
}

func (s *Service) deleteRow(ctx context.Context, q string, id int64) error {
	res, err := s.st.DB().ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- disable, restore ----------------------------------------------------------

// Disable cancels any pending apply, unhooks and flushes the owned chains in both
// families, and forgets the confirmed ruleset so a reboot does not bring it back.
func (m *Manager) Disable(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopTimer()
	return disableAll(ctx, m.st, m.cmd, m.ipv6)
}

// DisableAll is the console recovery path (`panel firewall disable`): it needs only
// the database and the iptables tools, not the daemon.
func DisableAll(ctx context.Context, st *store.Store, cmd Cmd) error {
	return disableAll(ctx, st, cmd, HostHasIPv6())
}

func disableAll(ctx context.Context, st *store.Store, cmd Cmd, ipv6 bool) error {
	var errs []error
	for _, fc := range families(ipv6) {
		for range 8 {
			if cmd.RunStdin(ctx, "", fc.bin, "-C", "INPUT", "-j", ChainInput) != nil {
				break
			}
			if err := cmd.RunStdin(ctx, "", fc.bin, "-D", "INPUT", "-j", ChainInput); err != nil {
				errs = append(errs, err)
				break
			}
		}
		unhookDocker(ctx, cmd, fc)
		if err := cmd.RunStdin(ctx, restoreText(snapshot{}), fc.restore, "--noflush"); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return st.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM fw_pending WHERE id = 1`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE fw_state SET enabled = 0, confirmed_v4 = '', confirmed_v6 = '', confirmed_at = '' WHERE id = 1`)
		return err
	})
}

// RestoreConfirmed replays the last confirmed ruleset (`panel firewall restore`, run
// by panel-firewall.service before the network comes up). It needs only the database
// and the iptables tools. A firewall that is not enabled is left alone, and running
// it twice never stacks the INPUT jump.
func RestoreConfirmed(ctx context.Context, st *store.Store, cmd Cmd) error {
	var en int
	var text [2]string
	if err := st.DB().QueryRowContext(ctx, `SELECT enabled, confirmed_v4, confirmed_v6 FROM fw_state WHERE id = 1`).
		Scan(&en, &text[0], &text[1]); err != nil {
		return err
	}
	if en != 1 {
		return nil
	}
	var errs []error
	for i, fc := range families(HostHasIPv6()) {
		t := text[i]
		if t == "" {
			continue
		}
		if cmd.RunStdin(ctx, "", fc.bin, "-C", "INPUT", "-j", ChainInput) == nil {
			t = stripHook(t)
		}
		if err := cmd.RunStdin(ctx, t, fc.restore, "--noflush"); err != nil {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, hookDocker(ctx, cmd, fc))
	}
	return errors.Join(errs...)
}

func stripHook(text string) string {
	var b strings.Builder
	for l := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(l, "-I INPUT ") {
			continue
		}
		b.WriteString(l + "\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func families(ipv6 bool) []famCmds {
	fs := []famCmds{{fam4, "iptables", "iptables-save", "iptables-restore"}}
	if ipv6 {
		fs = append(fs, famCmds{fam6, "ip6tables", "ip6tables-save", "ip6tables-restore"})
	}
	return fs
}

// HostHasIPv6 reports whether the host has IPv6 and the ip6tables tools.
func HostHasIPv6() bool {
	if _, err := os.Stat("/proc/net/if_inet6"); err != nil {
		return false
	}
	_, err := exec.LookPath("ip6tables-restore")
	return err == nil
}

// --- Docker coexistence -----------------------------------------------------------

// PublishedPorts lists the ports non-panel containers publish. A failure to reach
// Docker returns nil: the warning is advisory.
func PublishedPorts(ctx context.Context, cmd Cmd) []PublishedPort {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := cmd.Output(ctx, "docker", "ps",
		"--filter", "label!=panel.managed=true",
		"--format", "{{.Names}}\t{{.Ports}}")
	if err != nil {
		return nil
	}
	return ParsePublishedPorts(string(out))
}

// ParsePublishedPorts reads `docker ps` name/ports lines such as
// "web\t0.0.0.0:8080->80/tcp, :::8080->80/tcp, 9000/tcp".
func ParsePublishedPorts(out string) []PublishedPort {
	seen := map[PublishedPort]bool{}
	var res []PublishedPort
	for line := range strings.SplitSeq(out, "\n") {
		name, ports, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		for p := range strings.SplitSeq(ports, ",") {
			p = strings.TrimSpace(p)
			host, _, found := strings.Cut(p, "->")
			if !found {
				continue // exposed but not published
			}
			i := strings.LastIndex(host, ":")
			if i < 0 {
				continue
			}
			portStr, proto := host[i+1:], p[strings.LastIndex(p, "/")+1:]
			if j := strings.Index(portStr, "-"); j >= 0 { // a published range: report its first port
				portStr = portStr[:j]
			}
			n, err := strconv.Atoi(portStr)
			if err != nil {
				continue
			}
			pp := PublishedPort{Container: name, Port: n, Protocol: proto}
			if !seen[pp] {
				seen[pp] = true
				res = append(res, pp)
			}
		}
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].Port != res[j].Port {
			return res[i].Port < res[j].Port
		}
		return res[i].Container < res[j].Container
	})
	return res
}
