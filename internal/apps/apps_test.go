package apps

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/store"
)

func intp(n int) *int { return &n }

func testPaths(root string) Paths { return Paths{Root: filepath.ToSlash(root)} }

func normalize(t *testing.T, in Input) App {
	t.Helper()
	a, err := in.Normalize(testPaths("/www").DefaultWorkDir)
	if err != nil {
		t.Fatalf("Normalize(%+v): %v", in, err)
	}
	return a
}

func TestNormalizeAppliesDefaults(t *testing.T) {
	a := normalize(t, Input{Name: "web", Type: TypeGo, Command: "/www/services/apps/web/bin/web"})
	if a.WorkDir != "/www/services/apps/web" || a.User != "www" || a.Restart != RestartAlways || !a.Autostart {
		t.Fatalf("defaults not applied: %+v", a)
	}
	if a.Args == nil || a.Env == nil {
		t.Fatalf("args and env must be non-nil so they marshal as [] and {}: %+v", a)
	}
}

func TestNormalizeRejects(t *testing.T) {
	cases := []struct {
		name  string
		in    Input
		field string
	}{
		{"uppercase name", Input{Name: "Web", Type: TypeGo, Command: "/bin/x"}, "name"},
		{"reserved name", Input{Name: "panel", Type: TypeGo, Command: "/bin/x"}, "name"},
		{"unknown type", Input{Name: "a", Type: "php", Command: "/bin/x"}, "type"},
		{"relative go command", Input{Name: "a", Type: TypeGo, Command: "bin/x"}, "command"},
		{"unclean command", Input{Name: "a", Type: TypeGo, Command: "/bin/../x"}, "command"},
		{"percent in command", Input{Name: "a", Type: TypeGo, Command: "/bin/x%(ENV_A)s"}, "command"},
		{"newline in arg", Input{Name: "a", Type: TypeGo, Command: "/bin/x", Args: []string{"a\nb"}}, "args[0]"},
		{"relative workdir", Input{Name: "a", Type: TypeGo, Command: "/bin/x", WorkDir: "srv"}, "workdir"},
		{"bad env key", Input{Name: "a", Type: TypeGo, Command: "/bin/x", Env: map[string]string{"1A": "x"}}, "env"},
		{"quote in env value", Input{Name: "a", Type: TypeGo, Command: "/bin/x", Env: map[string]string{"A": `x"y`}}, "env"},
		{"node without version", Input{Name: "a", Type: TypeNode, Command: "server.js"}, "node_version"},
		{"node bad version", Input{Name: "a", Type: TypeNode, Command: "server.js", NodeVersion: "22;rm"}, "node_version"},
		{"bad restart", Input{Name: "a", Type: TypeGo, Command: "/bin/x", Restart: "sometimes"}, "restart"},
		{"port zero", Input{Name: "a", Type: TypeGo, Command: "/bin/x", Port: intp(0)}, "port"},
		{"bad user", Input{Name: "a", Type: TypeGo, Command: "/bin/x", User: "Root User"}, "user"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.in.Normalize(testPaths("/www").DefaultWorkDir)
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != c.field {
				t.Fatalf("got %v, want a ValidationError on %q", err, c.field)
			}
		})
	}
}

func TestRenderGoldenGo(t *testing.T) {
	a := normalize(t, Input{
		Name: "web", Type: TypeGo, Command: "/www/services/apps/web/bin/web",
		Args: []string{"--listen", ":3000", "--title", "my app"},
		Env:  map[string]string{"MODE": "prod", "A_LIST": "x,y z"}, Port: intp(3000),
	})
	got := string(Render(a, testPaths("/www"), ""))
	want := `; Managed by Server Panel. Edit the app in the panel: this file is rewritten from it.
[program:web]
command=/www/services/apps/web/bin/web --listen :3000 --title 'my app'
directory=/www/services/apps/web
user=www
environment=A_LIST="x,y z",MODE="prod",PORT="3000"
autostart=true
autorestart=true
startsecs=3
startretries=5
stopsignal=TERM
stopwaitsecs=10
stdout_logfile=/www/panel/logs/apps/web/out.log
stdout_logfile_maxbytes=10MB
stdout_logfile_backups=3
redirect_stderr=true
`
	if got != want {
		t.Fatalf("program file:\n%s\nwant:\n%s", got, want)
	}
	if err := CheckProgramFile([]byte(got)); err != nil {
		t.Fatalf("a rendered file must pass its own validator: %v", err)
	}
}

func TestRenderNodeUsesTheNodeBinaryAndKeepsAnOperatorPort(t *testing.T) {
	off := false
	a := normalize(t, Input{
		Name: "api", Type: TypeNode, Command: "server.js", NodeVersion: "22",
		Env: map[string]string{"PORT": "8080"}, Port: intp(3000), Restart: RestartOnFailure,
		Autostart: &off,
	})
	got := string(Render(a, testPaths("/www"), "/www/panel/nvm/versions/node/v22.11.0/bin/node"))
	for _, want := range []string{
		"command=/www/panel/nvm/versions/node/v22.11.0/bin/node server.js\n",
		`environment=PORT="8080"` + "\n",
		"autostart=false\n",
		"autorestart=unexpected\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestQuoteHandlesAQuote(t *testing.T) {
	if got, want := quote("it's"), `'it'"'"'s'`; got != want {
		t.Fatalf("quote = %s, want %s", got, want)
	}
	if got := quote(""); got != "''" {
		t.Fatalf("an empty word must stay a word: %q", got)
	}
}

func TestCheckProgramFileRejects(t *testing.T) {
	for name, content := range map[string]string{
		"no section":      "command=/bin/x\n",
		"two sections":    "[program:a]\ncommand=/x\ndirectory=/\nuser=www\n[program:b]\n",
		"not a program":   "[group:a]\n",
		"unknown key":     "[program:a]\ncommand=/x\ndirectory=/\nuser=www\nnice=5\n",
		"missing command": "[program:a]\ndirectory=/\nuser=www\n",
		"no equals":       "[program:a]\ncommand\n",
	} {
		if err := CheckProgramFile([]byte(content)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// --- rpc -----------------------------------------------------------------------

func TestDecodeResponse(t *testing.T) {
	v, f, err := decodeResponse([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><boolean>1</boolean></value></param></params></methodResponse>`))
	if err != nil || f != nil || v != true {
		t.Fatalf("bool: %v %v %v", v, f, err)
	}

	_, f, err = decodeResponse([]byte(`<methodResponse><fault><value><struct>
		<member><name>faultCode</name><value><int>60</int></value></member>
		<member><name>faultString</name><value><string>ALREADY_STARTED: web</string></value></member>
		</struct></value></fault></methodResponse>`))
	if err != nil || f == nil || f.Code != 60 || f.String != "ALREADY_STARTED: web" {
		t.Fatalf("fault: %+v %v", f, err)
	}

	v, _, err = decodeResponse([]byte(`<methodResponse><params><param><value><array><data>
		<value><struct><member><name>name</name><value><string>web</string></value></member>
		<member><name>pid</name><value><int>42</int></value></member></struct></value>
		<value>plain text</value></data></array></value></param></params></methodResponse>`))
	if err != nil {
		t.Fatal(err)
	}
	list := v.([]any)
	if list[0].(map[string]any)["pid"] != 42 || list[1] != "plain text" {
		t.Fatalf("array: %#v", list)
	}
}

func TestEncodeCallEscapesAndTypes(t *testing.T) {
	body, err := encodeCall("supervisor.startProcess", []any{"a<b&c", true})
	if err != nil {
		t.Fatal(err)
	}
	want := `<?xml version="1.0"?><methodCall><methodName>supervisor.startProcess</methodName><params>` +
		`<param><value><string>a&lt;b&amp;c</string></value></param>` +
		`<param><value><boolean>1</boolean></value></param></params></methodCall>`
	if string(body) != want {
		t.Fatalf("body:\n%s\nwant:\n%s", body, want)
	}
}

// fakeRPC answers by method name with a canned reply and records the requests.
func fakeRPC(t *testing.T, replies map[string]string) (*Client, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		seen = append(seen, body)
		for method, reply := range replies {
			if strings.Contains(body, "<methodName>"+method+"</methodName>") {
				_, _ = io.WriteString(w, reply)
				return
			}
		}
		http.Error(w, "no canned reply", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.Client(), srv.URL), &seen
}

func wrap(value string) string {
	return `<methodResponse><params><param><value>` + value + `</value></param></params></methodResponse>`
}

func TestClientDecodesProcessesAndReload(t *testing.T) {
	c, _ := fakeRPC(t, map[string]string{
		"supervisor.getAllProcessInfo": wrap(`<array><data><value><struct>
			<member><name>name</name><value><string>web</string></value></member>
			<member><name>statename</name><value><string>RUNNING</string></value></member>
			<member><name>pid</name><value><int>77</int></value></member>
			<member><name>start</name><value><int>1000</int></value></member>
			<member><name>now</name><value><int>1090</int></value></member>
			</struct></value></data></array>`),
		"supervisor.reloadConfig": wrap(`<array><data><value><array><data>
			<value><array><data><value><string>a</string></value></data></array></value>
			<value><array><data></data></array></value>
			<value><array><data><value><string>z</string></value></data></array></value>
			</data></array></value></data></array>`),
		"supervisor.getState": wrap(`<struct><member><name>statename</name><value><string>RUNNING</string></value></member>
			<member><name>statecode</name><value><int>1</int></value></member></struct>`),
	})
	ctx := context.Background()
	procs, err := c.Processes(ctx)
	if err != nil || len(procs) != 1 || procs[0].PID != 77 || !procs[0].Running() || procs[0].Now-procs[0].Start != 90 {
		t.Fatalf("processes: %+v %v", procs, err)
	}
	diff, err := c.ReloadConfig(ctx)
	if err != nil || !reflect.DeepEqual(diff, ConfigDiff{Added: []string{"a"}, Changed: []string{}, Removed: []string{"z"}}) {
		t.Fatalf("reload: %+v %v", diff, err)
	}
	if state, err := c.State(ctx); err != nil || state != "RUNNING" {
		t.Fatalf("state: %q %v", state, err)
	}
}

func TestClientMapsFaultsAndAcceptsBothBoolShapes(t *testing.T) {
	fault := func(code, text string) string {
		return `<methodResponse><fault><value><struct><member><name>faultCode</name><value><int>` + code +
			`</int></value></member><member><name>faultString</name><value><string>` + text +
			`</string></value></member></struct></value></fault></methodResponse>`
	}
	c, seen := fakeRPC(t, map[string]string{
		"supervisor.startProcess":       fault("60", "ALREADY_STARTED"),
		"supervisor.stopProcess":        fault("70", "NOT_RUNNING"),
		"supervisor.removeProcessGroup": fault("10", "BAD_NAME"),
		"supervisor.addProcessGroup":    wrap(`<struct><member><name>Success</name><value><boolean>1</boolean></value></member></struct>`),
	})
	ctx := context.Background()
	if err := c.Start(ctx, "web"); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("start: %v", err)
	}
	if err := c.Stop(ctx, "web"); !errors.Is(err, ErrNotRunning) {
		t.Errorf("stop: %v", err)
	}
	if err := c.RemoveGroup(ctx, "web"); !errors.Is(err, ErrNoProcess) {
		t.Errorf("remove: %v", err)
	}
	if err := c.AddGroup(ctx, "web"); err != nil {
		t.Errorf("a one-member struct reply must be accepted: %v", err)
	}
	if !strings.Contains((*seen)[0], "<boolean>1</boolean>") {
		t.Errorf("start must ask supervisord to wait: %s", (*seen)[0])
	}
}

func TestClientReportsAnUnreachableSocket(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	if _, err := c.State(context.Background()); !errors.Is(err, ErrNoSupervisor) {
		t.Fatalf("got %v, want ErrNoSupervisor", err)
	}
}

// --- manager over a fake supervisord ------------------------------------------

// fakeSupervisor follows the semantics read from the upstream source: reloadConfig
// compares the files on disk with what is loaded and changes nothing,
// addProcessGroup loads a program stopped, and removeProcessGroup stops and
// forgets it.
type fakeSupervisor struct {
	dir       string
	loaded    map[string]string // name -> program file content
	state     map[string]string // name -> process state
	starts    map[string]int
	reloadErr error
	failStart map[string]bool
}

func newFakeSupervisor(dir string) *fakeSupervisor {
	return &fakeSupervisor{dir: dir, loaded: map[string]string{}, state: map[string]string{},
		starts: map[string]int{}, failStart: map[string]bool{}}
}

func (f *fakeSupervisor) pending() map[string]string {
	out := map[string]string{}
	entries, _ := os.ReadDir(f.dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".conf") {
			b, _ := os.ReadFile(filepath.Join(f.dir, e.Name()))
			out[strings.TrimSuffix(e.Name(), ".conf")] = string(b)
		}
	}
	return out
}

func (f *fakeSupervisor) State(context.Context) (string, error) { return "RUNNING", nil }

func (f *fakeSupervisor) Processes(context.Context) ([]Process, error) {
	var out []Process
	for name := range f.loaded {
		out = append(out, Process{Name: name, State: f.state[name], PID: 100, Start: 10, Now: 70})
	}
	return out, nil
}

func (f *fakeSupervisor) ReloadConfig(context.Context) (ConfigDiff, error) {
	if f.reloadErr != nil {
		return ConfigDiff{}, f.reloadErr
	}
	var d ConfigDiff
	pend := f.pending()
	for name, content := range pend {
		if old, ok := f.loaded[name]; !ok {
			d.Added = append(d.Added, name)
		} else if old != content {
			d.Changed = append(d.Changed, name)
		}
	}
	for name := range f.loaded {
		if _, ok := pend[name]; !ok {
			d.Removed = append(d.Removed, name)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Changed)
	sort.Strings(d.Removed)
	return d, nil
}

func (f *fakeSupervisor) AddGroup(_ context.Context, name string) error {
	f.loaded[name] = f.pending()[name]
	f.state[name] = "STOPPED"
	return nil
}

func (f *fakeSupervisor) RemoveGroup(_ context.Context, name string) error {
	if _, ok := f.loaded[name]; !ok {
		return ErrNoProcess
	}
	delete(f.loaded, name)
	delete(f.state, name)
	return nil
}

func (f *fakeSupervisor) Start(_ context.Context, name string) error {
	if _, ok := f.loaded[name]; !ok {
		return ErrNoProcess
	}
	if f.state[name] == "RUNNING" {
		return ErrAlreadyRunning
	}
	if f.failStart[name] {
		f.state[name] = "FATAL"
		return errors.New("exited too quickly")
	}
	f.starts[name]++
	f.state[name] = "RUNNING"
	return nil
}

func (f *fakeSupervisor) Stop(_ context.Context, name string) error {
	if f.state[name] != "RUNNING" {
		return ErrNotRunning
	}
	f.state[name] = "STOPPED"
	return nil
}

type fakeNode struct{ versions map[string]string }

func (n fakeNode) NodePath(v string) (string, error) {
	if p, ok := n.versions[v]; ok {
		return p, nil
	}
	return "", errors.New("not installed")
}

type rig struct {
	m    *Manager
	sv   *fakeSupervisor
	p    Paths
	conf string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	p := testPaths(root)
	conf := filepath.FromSlash(p.ConfDir())
	if err := os.MkdirAll(conf, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ed, err := confedit.New(confedit.Options{Roots: []string{p.ConfDir()}, HistoryDir: filepath.Join(root, "history"), Store: st})
	if err != nil {
		t.Fatal(err)
	}
	sv := newFakeSupervisor(conf)
	m, err := New(Options{Store: st, Applier: ed, Supervisor: sv, Paths: p,
		Node: fakeNode{versions: map[string]string{"22": "/node22/bin/node"}}})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{m: m, sv: sv, p: p, conf: conf}
}

func goApp(name string) Input {
	return Input{Name: name, Type: TypeGo, Command: "/bin/" + name, Port: intp(3000)}
}

func TestCreateWritesTheFileLoadsAndStartsTheApp(t *testing.T) {
	r := newRig(t)
	st, warns, err := r.m.Create(context.Background(), goApp("web"))
	if err != nil || len(warns) != 0 {
		t.Fatalf("create: %v %v", err, warns)
	}
	if st.State != "RUNNING" || st.PID != 100 || st.UptimeSecs != 60 || st.ProxyURL != "http://127.0.0.1:3000" {
		t.Fatalf("status: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(r.conf, "web.conf")); err != nil {
		t.Fatalf("program file: %v", err)
	}
	if r.sv.starts["web"] != 1 {
		t.Fatalf("starts = %d", r.sv.starts["web"])
	}
}

func TestCreateWithoutAutostartLoadsButDoesNotStart(t *testing.T) {
	r := newRig(t)
	in := goApp("web")
	off := false
	in.Autostart = &off
	st, _, err := r.m.Create(context.Background(), in)
	if err != nil || st.State != "STOPPED" || r.sv.starts["web"] != 0 {
		t.Fatalf("status %+v starts %d err %v", st, r.sv.starts["web"], err)
	}
}

func TestCreateRejectsDuplicatesAndPortClashes(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, _, err := r.m.Create(ctx, goApp("web")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.m.Create(ctx, goApp("web")); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, _, err := r.m.Create(ctx, goApp("other")); !errors.Is(err, ErrPortInUse) {
		t.Fatalf("port clash: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.conf, "other.conf")); !os.IsNotExist(err) {
		t.Fatalf("a refused app left its program file behind: %v", err)
	}
}

func TestCreateRollsBackWhenSupervisordRejectsTheFile(t *testing.T) {
	r := newRig(t)
	r.sv.reloadErr = errors.New("line 3: bad value")
	_, _, err := r.m.Create(context.Background(), goApp("web"))
	var ae *confedit.ApplyError
	if !errors.As(err, &ae) || ae.Stage != confedit.StageValidate || !strings.Contains(ae.Output, "bad value") {
		t.Fatalf("want a validate-stage ApplyError carrying supervisord's output, got %v", err)
	}
	if _, err := r.m.Get(context.Background(), "web"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a rejected app must not be stored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.conf, "web.conf")); !os.IsNotExist(err) {
		t.Fatal("a rejected app left its program file behind")
	}
}

func TestAProgramThatWillNotStartIsAWarningNotAFailure(t *testing.T) {
	r := newRig(t)
	r.sv.failStart["web"] = true
	st, warns, err := r.m.Create(context.Background(), goApp("web"))
	if err != nil {
		t.Fatalf("create must succeed so the operator can fix the program: %v", err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "did not start") || st.State != "FATAL" {
		t.Fatalf("warnings %v state %s", warns, st.State)
	}
}

func TestUpdateRestartsARunningAppAndRestoresOnFailure(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, _, err := r.m.Create(ctx, goApp("web")); err != nil {
		t.Fatal(err)
	}
	in := goApp("web")
	in.Args = []string{"--new"}
	st, _, err := r.m.Update(ctx, "web", in)
	if err != nil || st.State != "RUNNING" || r.sv.starts["web"] != 2 {
		t.Fatalf("update: %+v starts=%d err=%v", st, r.sv.starts["web"], err)
	}
	if got, _ := os.ReadFile(filepath.Join(r.conf, "web.conf")); !strings.Contains(string(got), "--new") {
		t.Fatalf("file not rewritten:\n%s", got)
	}

	r.sv.reloadErr = errors.New("boom")
	in.Args = []string{"--bad"}
	if _, _, err := r.m.Update(ctx, "web", in); err == nil {
		t.Fatal("update should fail")
	}
	r.sv.reloadErr = nil
	got, _ := r.m.Get(ctx, "web")
	if !reflect.DeepEqual(got.Args, []string{"--new"}) {
		t.Fatalf("a failed update must keep the old definition: %v", got.Args)
	}
	if b, _ := os.ReadFile(filepath.Join(r.conf, "web.conf")); strings.Contains(string(b), "--bad") {
		t.Fatal("a failed update left the new file behind")
	}
}

func TestUpdateRefusesRename(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, _, err := r.m.Create(ctx, goApp("web")); err != nil {
		t.Fatal(err)
	}
	in := goApp("web2")
	if _, _, err := r.m.Update(ctx, "web", in); err == nil {
		t.Fatal("rename accepted")
	}
}

func TestDeleteStopsAndForgetsTheApp(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, _, err := r.m.Create(ctx, goApp("web")); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Delete(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.sv.loaded["web"]; ok {
		t.Fatal("supervisord still has the program")
	}
	if _, err := os.Stat(filepath.Join(r.conf, "web.conf")); !os.IsNotExist(err) {
		t.Fatal("program file remains")
	}
	if err := r.m.Delete(ctx, "web"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestNodeAppNeedsAnInstalledVersion(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	in := Input{Name: "api", Type: TypeNode, Command: "server.js", NodeVersion: "18"}
	if _, _, err := r.m.Create(ctx, in); !errors.Is(err, ErrNodeMissing) {
		t.Fatalf("got %v, want ErrNodeMissing", err)
	}
	in.NodeVersion = "22"
	if _, _, err := r.m.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(r.conf, "api.conf"))
	if !strings.Contains(string(b), "command=/node22/bin/node server.js") {
		t.Fatalf("node binary not used:\n%s", b)
	}
}

func TestControls(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, _, err := r.m.Create(ctx, goApp("web")); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Stop(ctx, "web"); err != nil || r.sv.state["web"] != "STOPPED" {
		t.Fatalf("stop: %v %s", err, r.sv.state["web"])
	}
	if err := r.m.Stop(ctx, "web"); err != nil {
		t.Fatalf("stopping a stopped app is not an error: %v", err)
	}
	if err := r.m.Restart(ctx, "web"); err != nil || r.sv.state["web"] != "RUNNING" {
		t.Fatalf("restart of a stopped app: %v %s", err, r.sv.state["web"])
	}
	if err := r.m.Start(ctx, "nosuch"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown app: %v", err)
	}
}

func TestLogTailAndFollow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, _, err := r.m.Create(ctx, goApp("web")); err != nil {
		t.Fatal(err)
	}
	if c, err := r.m.Log(ctx, "web", -1, 0); err != nil || c.Data != "" || c.Offset != 0 {
		t.Fatalf("a missing log is empty: %+v %v", c, err)
	}
	path := filepath.FromSlash(r.p.LogFile("web"))
	if err := os.WriteFile(path, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := r.m.Log(ctx, "web", -1, 8)
	if err != nil || c.Data != "1\nline2\n" || c.Offset != 12 {
		t.Fatalf("tail of the last 8 bytes: %+v %v", c, err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("line3\n")
	_ = f.Close()
	c, err = r.m.Log(ctx, "web", 12, 0)
	if err != nil || c.Data != "line3\n" || c.Offset != 18 {
		t.Fatalf("follow: %+v %v", c, err)
	}
	c, _ = r.m.Log(ctx, "web", 999, 0)
	if c.Offset != 18 || !strings.HasSuffix(c.Data, "line3\n") {
		t.Fatalf("an offset past the end means the log rotated: %+v", c)
	}
}

// The Go supervisord reports "Running", not RUNNING: seen on a live daemon.
func TestProcessStateIsNormalisedToUpperCase(t *testing.T) {
	c, _ := fakeRPC(t, map[string]string{
		"supervisor.getAllProcessInfo": wrap(`<array><data><value><struct>
			<member><name>name</name><value><string>web</string></value></member>
			<member><name>statename</name><value><string>Running</string></value></member>
			</struct></value></data></array>`),
		"supervisor.getState": wrap(`<struct><member><name>statename</name><value><string>RUNNING</string></value></member></struct>`),
	})
	procs, err := c.Processes(context.Background())
	if err != nil || len(procs) != 1 || procs[0].State != "RUNNING" || !procs[0].Running() {
		t.Fatalf("processes: %+v %v", procs, err)
	}
	rel := &reloader{sv: c}
	if err := rel.Healthy(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The Go supervisord writes out.log.<timestamp> and never the plain name.
func TestLogFollowsTheNewestTimestampedFile(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, _, err := r.m.Create(ctx, goApp("web")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.FromSlash(r.p.LogDir("web"))
	old := filepath.Join(dir, "out.log.2026-11-17T06-57-43")
	cur := filepath.Join(dir, "out.log.2026-11-17T06-58-10")
	if err := os.WriteFile(old, []byte("old run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cur, []byte("current run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	c, err := r.m.Log(ctx, "web", -1, 0)
	if err != nil || c.Data != "current run\n" {
		t.Fatalf("got %+v %v", c, err)
	}
}
