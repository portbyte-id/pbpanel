package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fakeEngine serves h and returns a client for it. On Linux, where the daemon
// runs, the server listens on a real Unix socket so the tests cover the
// transport the daemon uses. Where a Unix socket cannot be dialled (Windows
// developer machines) the same handler is served on loopback TCP through the
// client's test-only dial seam: every request path is still covered there, the
// dial itself is not.
func fakeEngine(t *testing.T, h http.Handler) *Client {
	t.Helper()
	// Not t.TempDir(): a socket path is limited to about a hundred bytes and
	// the test's temp directory carries the full test name.
	dir, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	sock := filepath.Join(dir, "d.sock")
	c, err := New(sock)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ln, err := net.Listen("unix", sock)
	if err == nil {
		if probe, derr := net.Dial("unix", sock); derr == nil {
			_ = probe.Close()
		} else {
			_ = ln.Close()
			err = derr
		}
	}
	if err != nil {
		if runtime.GOOS == "linux" {
			t.Fatalf("unix socket: %v", err)
		}
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		addr := ln.Addr().String()
		c.dial = func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		}
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		c.Close()
		_ = srv.Close()
		_ = os.RemoveAll(dir)
	})
	return c
}

// frame is one frame of a multiplexed stream.
func frame(stream byte, payload string) []byte {
	head := make([]byte, frameHeaderLen)
	head[0] = stream
	binary.BigEndian.PutUint32(head[4:], uint32(len(payload)))
	return append(head, payload...)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const v = "/v" + apiVersion

func TestNewRejectsAnythingButASocketPath(t *testing.T) {
	for _, bad := range []string{"", "tcp://10.0.0.1:2375", "http://localhost:2375", "unix:///var/run/docker.sock", "docker.sock", "run/docker.sock"} {
		if _, err := New(bad); err == nil {
			t.Errorf("New(%q) succeeded, want an error", bad)
		}
	}
}

func TestPing(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		wantErr string
	}{
		{"current engine", "1.54", ""},
		{"exactly the floor", apiVersion, ""},
		{"too old", "1.43", "upgrade Docker Engine"},
		{"older major", "0.99", "upgrade Docker Engine"},
		{"no version reported", "", "did not report"},
		{"garbage version", "new", "bad API version"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Ping is the one unversioned call: it is how the version is learned.
				if r.URL.Path != "/_ping" {
					t.Errorf("ping path = %q, want /_ping", r.URL.Path)
				}
				if tc.header != "" {
					w.Header().Set("API-Version", tc.header)
				}
				_, _ = io.WriteString(w, "OK")
			}))
			info, err := c.Ping(context.Background())
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Ping: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Ping error = %v, want one containing %q", err, tc.wantErr)
			case tc.wantErr == "" && info.APIVersion != tc.header:
				t.Fatalf("APIVersion = %q, want %q", info.APIVersion, tc.header)
			}
		})
	}
}

func TestPingReportsAMissingSocket(t *testing.T) {
	c, err := New(filepath.Join(t.TempDir(), "absent.sock"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Ping(context.Background()); err == nil {
		t.Fatal("Ping against a missing socket succeeded")
	}
}

func TestErrorsKeepTheEngineMessage(t *testing.T) {
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, v+"/") {
			t.Errorf("path %q is not pinned to %s", r.URL.Path, v)
		}
		switch {
		case strings.Contains(r.URL.Path, "missing"):
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such image: missing:1"})
		case strings.Contains(r.URL.Path, "taken"):
			writeJSON(w, http.StatusConflict, map[string]string{"message": "name is in use"})
		default:
			http.Error(w, "plain text failure", http.StatusInternalServerError)
		}
	}))
	ctx := context.Background()

	_, err := c.ImageInspect(ctx, "missing:1")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "No such image: missing:1") {
		t.Fatalf("404 error = %v, want ErrNotFound carrying the Engine message", err)
	}
	if errors.Is(err, ErrConflict) {
		t.Fatal("a 404 matched ErrConflict")
	}
	if _, err := c.ContainerInspect(ctx, "taken"); !errors.Is(err, ErrConflict) {
		t.Fatalf("409 error = %v, want ErrConflict", err)
	}
	_, err = c.ContainerInspect(ctx, "other")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 || apiErr.Message != "plain text failure" {
		t.Fatalf("500 error = %v, want an APIError with the raw body", err)
	}
}

func TestInvalidNamesNeverReachTheEngine(t *testing.T) {
	c := fakeEngine(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("request reached the Engine: %s %s", r.Method, r.URL.Path)
	}))
	ctx := context.Background()
	if _, err := c.ContainerInspect(ctx, "a/../../images/x"); err == nil {
		t.Error("a container name with slashes was accepted")
	}
	if _, err := c.ImageInspect(ctx, "nginx/../../containers/x"); err == nil {
		t.Error("an image reference with .. was accepted")
	}
	if _, err := c.ImageInspect(ctx, "nginx?all=1"); err == nil {
		t.Error("an image reference with a query was accepted")
	}
	if err := c.ContainerKill(ctx, "panel-nginx", "HUP&x=1"); err == nil {
		t.Error("a signal with a query was accepted")
	}
}

func TestImagePull(t *testing.T) {
	const present = `{"Id":"sha256:abc","RepoTags":["nginx:1.29"]}`
	tests := []struct {
		name      string
		stream    string
		inspect   int
		wantErr   string
		wantLines []string
	}{
		{
			name: "success collapses repeated layer progress",
			stream: `{"status":"Pulling from library/nginx","id":"1.29"}
{"status":"Downloading","id":"aaa","progressDetail":{"current":1}}
{"status":"Downloading","id":"aaa","progressDetail":{"current":2}}
{"status":"Pull complete","id":"aaa"}
{"status":"Status: Downloaded newer image for nginx:1.29"}
`,
			inspect:   200,
			wantLines: []string{"1.29: Pulling from library/nginx", "aaa: Downloading", "aaa: Pull complete", "Status: Downloaded newer image for nginx:1.29"},
		},
		{
			name:    "an error line inside a 200 fails the pull",
			stream:  `{"status":"Pulling"}` + "\n" + `{"errorDetail":{"message":"manifest unknown"},"error":"manifest unknown"}` + "\n",
			inspect: 200,
			wantErr: "manifest unknown",
		},
		{
			name:    "a clean stream with no image afterwards fails the pull",
			stream:  `{"status":"Pulling"}` + "\n",
			inspect: 404,
			wantErr: "not present",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case v + "/images/create":
					if got := r.URL.Query().Get("fromImage") + ":" + r.URL.Query().Get("tag"); got != "nginx:1.29" {
						t.Errorf("pull asked for %q", got)
					}
					_, _ = io.WriteString(w, tc.stream)
				case v + "/images/nginx:1.29/json":
					if tc.inspect != 200 {
						writeJSON(w, tc.inspect, map[string]string{"message": "No such image"})
						return
					}
					_, _ = io.WriteString(w, present)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			var lines []string
			img, err := c.ImagePull(context.Background(), "nginx:1.29", func(l string) { lines = append(lines, l) })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ImagePull: %v", err)
			}
			if img.ID != "sha256:abc" {
				t.Errorf("image id = %q", img.ID)
			}
			if strings.Join(lines, "|") != strings.Join(tc.wantLines, "|") {
				t.Errorf("progress = %q\nwant       %q", lines, tc.wantLines)
			}
		})
	}
}

func TestSplitRef(t *testing.T) {
	tests := []struct{ ref, name, tag string }{
		{"nginx", "nginx", "latest"},
		{"nginx:1.29", "nginx", "1.29"},
		{"registry.local:5000/team/nginx", "registry.local:5000/team/nginx", "latest"},
		{"registry.local:5000/team/nginx:1.29", "registry.local:5000/team/nginx", "1.29"},
		{"nginx@sha256:abc", "nginx@sha256:abc", ""},
	}
	for _, tc := range tests {
		if name, tag := splitRef(tc.ref); name != tc.name || tag != tc.tag {
			t.Errorf("splitRef(%q) = %q, %q; want %q, %q", tc.ref, name, tag, tc.name, tc.tag)
		}
	}
}

func TestImageBuild(t *testing.T) {
	const dockerfile = "FROM nginx:1.29\nRUN useradd -u 1500 www\n"
	tests := []struct {
		name    string
		stream  string
		inspect int
		wantErr string
	}{
		{"success", `{"stream":"Step 1/2 : FROM nginx:1.29\n"}` + "\n" + `{"stream":"Successfully built 5f80\n"}` + "\n", 200, ""},
		{
			"a failed step inside a 200 fails the build",
			`{"stream":"Step 2/2 : RUN false\n"}` + "\n" + `{"errorDetail":{"code":1,"message":"The command returned a non-zero code: 1"},"error":"x"}` + "\n",
			200, "returned a non-zero code: 1",
		},
		{"a clean stream with no image afterwards fails the build", `{"stream":"Step 1/2\n"}` + "\n", 404, "not present"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case v + "/build":
					q := r.URL.Query()
					if q.Get("t") != "panel/nginx:1.29" || q.Get("version") != "1" || q.Get("labels") != `{"panel.managed":"true"}` {
						t.Errorf("build query = %v", q)
					}
					tr := tar.NewReader(r.Body)
					hdr, err := tr.Next()
					if err != nil || hdr.Name != "Dockerfile" {
						t.Errorf("first tar entry = %v, %v; want Dockerfile", hdr, err)
						return
					}
					body, _ := io.ReadAll(tr)
					if string(body) != dockerfile {
						t.Errorf("Dockerfile in context = %q", body)
					}
					if _, err := tr.Next(); !errors.Is(err, io.EOF) {
						t.Errorf("context has more than the Dockerfile: %v", err)
					}
					_, _ = io.WriteString(w, tc.stream)
				case v + "/images/panel/nginx:1.29/json":
					if tc.inspect != 200 {
						writeJSON(w, tc.inspect, map[string]string{"message": "No such image"})
						return
					}
					_, _ = io.WriteString(w, `{"Id":"sha256:built"}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			var lines []string
			img, err := c.ImageBuild(context.Background(), BuildOptions{
				Tag: "panel/nginx:1.29", Dockerfile: []byte(dockerfile), Labels: map[string]string{LabelManaged: "true"},
			}, func(l string) { lines = append(lines, l) })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ImageBuild: %v", err)
			}
			if img.ID != "sha256:built" || len(lines) != 2 || lines[1] != "Successfully built 5f80" {
				t.Errorf("image = %+v, progress = %q", img, lines)
			}
		})
	}
}

func TestContainerCreateSendsTheWholeConfig(t *testing.T) {
	var got map[string]any
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != v+"/containers/create" || r.URL.Query().Get("name") != "panel-nginx" {
			t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		writeJSON(w, http.StatusCreated, map[string]string{"Id": "c0ffee"})
	}))
	id, err := c.ContainerCreate(context.Background(), "panel-nginx", ContainerConfig{
		Image: "panel/nginx:1.29", Entrypoint: []string{"nginx"}, Cmd: []string{"-g", "daemon off;"},
		Labels: map[string]string{"panel.role": "nginx"}, Binds: []string{"/www/panel/nginx:/www/panel/nginx:ro"},
		NetworkMode: "host", RestartPolicy: "unless-stopped",
	})
	if err != nil || id != "c0ffee" {
		t.Fatalf("ContainerCreate = %q, %v", id, err)
	}
	host, _ := got["HostConfig"].(map[string]any)
	want := `{"Binds":["/www/panel/nginx:/www/panel/nginx:ro"],"NetworkMode":"host","RestartPolicy":{"Name":"unless-stopped"}}`
	if raw, _ := json.Marshal(host); string(raw) != want {
		t.Errorf("HostConfig = %s\nwant         %s", raw, want)
	}
	if cmd, _ := json.Marshal(got["Cmd"]); string(cmd) != `["-g","daemon off;"]` {
		t.Errorf("Cmd = %s: the argument vector must arrive unjoined", cmd)
	}
	if got["Image"] != "panel/nginx:1.29" {
		t.Errorf("Image = %v", got["Image"])
	}
}

func TestStartAndStopTreatNotModifiedAsDone(t *testing.T) {
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stop") && r.URL.Query().Get("t") != "15" {
			t.Errorf("stop timeout = %q, want 15", r.URL.Query().Get("t"))
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	if err := c.ContainerStart(context.Background(), "panel-nginx"); err != nil {
		t.Errorf("start of a running container: %v", err)
	}
	if err := c.ContainerStop(context.Background(), "panel-nginx", stopTimeout); err != nil {
		t.Errorf("stop of a stopped container: %v", err)
	}
}

func TestContainerKillSendsTheSignal(t *testing.T) {
	var signal string
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != v+"/containers/panel-nginx/kill" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		signal = r.URL.Query().Get("signal")
		w.WriteHeader(http.StatusNoContent)
	}))
	if err := c.ContainerKill(context.Background(), "panel-nginx", "HUP"); err != nil || signal != "HUP" {
		t.Fatalf("kill: signal %q, err %v", signal, err)
	}
}

func TestContainerInspect(t *testing.T) {
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Id":"c0ffee","Name":"/panel-nginx","Image":"sha256:img",
			"Config":{"Labels":{"panel.managed":"true"}},
			"State":{"Status":"running","Running":true,"Pid":4242,"StartedAt":"2026-11-17T01:02:03.123456789Z"}}`)
	}))
	got, err := c.ContainerInspect(context.Background(), "panel-nginx")
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	if got.ID != "c0ffee" || got.Name != "panel-nginx" || got.ImageID != "sha256:img" ||
		!got.State.Running || got.State.Pid != 4242 || got.Labels[LabelManaged] != "true" || got.State.StartedAt.Year() != 2026 {
		t.Errorf("inspect = %+v", got)
	}
}

func TestExecCapturesBothStreamsAndTheExitCode(t *testing.T) {
	var cmd []string
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case v + "/containers/panel-nginx/exec":
			var body struct {
				Cmd                        []string
				AttachStdout, AttachStderr bool
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !body.AttachStdout || !body.AttachStderr {
				t.Error("exec did not attach both output streams")
			}
			cmd = body.Cmd
			writeJSON(w, http.StatusCreated, map[string]string{"Id": "e1"})
		case v + "/exec/e1/start":
			w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
			_, _ = w.Write(frame(streamStdout, "out-1 "))
			_, _ = w.Write(frame(streamStderr, "nginx: [emerg] unknown directive\n"))
			_, _ = w.Write(frame(streamStdout, "out-2"))
		case v + "/exec/e1/json":
			writeJSON(w, 200, map[string]any{"Running": false, "ExitCode": 1})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	res, err := c.Exec(context.Background(), "panel-nginx", ExecOptions{Cmd: []string{"nginx", "-t", "-c", "/www/panel/nginx/nginx.conf"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 1 || string(res.Stdout) != "out-1 out-2" || string(res.Stderr) != "nginx: [emerg] unknown directive\n" || res.Truncated {
		t.Errorf("result = %+v", res)
	}
	if strings.Join(cmd, "|") != "nginx|-t|-c|/www/panel/nginx/nginx.conf" {
		t.Errorf("command arrived as %q", cmd)
	}
}

func TestExecRejectsAnEmptyCommand(t *testing.T) {
	c := fakeEngine(t, http.NotFoundHandler())
	if _, err := c.Exec(context.Background(), "panel-nginx", ExecOptions{}); err == nil {
		t.Fatal("an empty command was accepted")
	}
}

func TestDemux(t *testing.T) {
	var out, errOut bytes.Buffer
	src := append(frame(streamStdout, "a"), frame(streamStderr, "b")...)
	if err := demux(bytes.NewReader(src), &out, &errOut); err != nil || out.String() != "a" || errOut.String() != "b" {
		t.Fatalf("demux = %q, %q, %v", out.String(), errOut.String(), err)
	}
	// A TTY container's output is raw text, not frames. It must be refused,
	// not read as a frame header with a huge length.
	if err := demux(strings.NewReader("plain text from a tty container"), io.Discard, io.Discard); err == nil {
		t.Error("unframed text was accepted as a multiplexed stream")
	}
	if err := demux(bytes.NewReader(frame(streamStdout, "abc")[:10]), io.Discard, io.Discard); err == nil {
		t.Error("a frame cut short was accepted")
	}
}

func TestCapBufferKeepsTheStartAndFlagsTheRest(t *testing.T) {
	b := &capBuffer{limit: 5}
	for _, s := range []string{"abc", "defg", "h"} {
		if n, err := b.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write(%q) = %d, %v: a full buffer must keep accepting so the stream drains", s, n, err)
		}
	}
	if string(b.buf) != "abcde" || !b.truncated {
		t.Errorf("buffer = %q, truncated = %v", b.buf, b.truncated)
	}
}

func TestRunOnceAlwaysRemovesTheContainer(t *testing.T) {
	for _, startFails := range []bool{false, true} {
		var (
			mu    sync.Mutex
			calls []string
		)
		c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			calls = append(calls, r.Method+" "+strings.TrimPrefix(r.URL.Path, v))
			mu.Unlock()
			switch {
			case strings.HasSuffix(r.URL.Path, "/create"):
				var body struct {
					HostConfig struct{ RestartPolicy struct{ Name string } }
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body.HostConfig.RestartPolicy.Name != "" {
					t.Errorf("a one-shot container got restart policy %q", body.HostConfig.RestartPolicy.Name)
				}
				writeJSON(w, http.StatusCreated, map[string]string{"Id": "once1"})
			case strings.HasSuffix(r.URL.Path, "/start"):
				if startFails {
					writeJSON(w, 500, map[string]string{"message": "exec: \"nginx\": not found"})
					return
				}
				w.WriteHeader(http.StatusNoContent)
			case strings.HasSuffix(r.URL.Path, "/wait"):
				writeJSON(w, 200, map[string]int{"StatusCode": 1})
			case strings.HasSuffix(r.URL.Path, "/logs"):
				_, _ = w.Write(frame(streamStderr, "nginx: configuration file test failed\n"))
			case r.Method == http.MethodDelete:
				w.WriteHeader(http.StatusNoContent)
			}
		}))
		res, err := c.RunOnce(context.Background(), "panel-nginx-check", ContainerConfig{Image: "panel/nginx:1.29", RestartPolicy: "unless-stopped"})
		if startFails {
			if err == nil || !strings.Contains(err.Error(), "not found") {
				t.Errorf("start failure: err = %v", err)
			}
		} else if err != nil || res.ExitCode != 1 || !strings.Contains(res.Output(), "test failed") {
			t.Errorf("RunOnce = %+v, %v", res, err)
		}
		mu.Lock()
		last := ""
		if len(calls) > 0 {
			last = calls[len(calls)-1]
		}
		mu.Unlock()
		if last != "DELETE /containers/once1" {
			t.Errorf("startFails=%v: last call was %q, want the container removed; calls: %q", startFails, last, calls)
		}
	}
}
