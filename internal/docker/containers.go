package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxExecOutput bounds what Exec and RunOnce keep per stream. Validator output
// (`nginx -t`) is a few lines; a megabyte leaves room for a long error list
// without letting a runaway command grow the daemon.
const maxExecOutput = 1 << 20

// ContainerConfig is what a container is created from. It is the wire-level
// view; callers describe containers with Spec, which decides these fields.
type ContainerConfig struct {
	Image      string
	User       string
	Entrypoint []string
	Cmd        []string
	Env        []string
	Labels     map[string]string
	StopSignal string
	// Binds are `host:container[:ro]` bind mounts.
	Binds []string
	// NetworkMode is `host` for every panel container (decision 9).
	NetworkMode string
	// RestartPolicy is a Docker restart policy name; empty means `no`.
	RestartPolicy string
}

// Container is the subset of a container inspect the panel uses.
type Container struct {
	ID   string
	Name string
	// ImageID is the content address of the image the container was created
	// from, which is how a rebuilt tag is told from the one in use.
	ImageID string
	Labels  map[string]string
	State   ContainerState
}

// ContainerState is a container's run state.
type ContainerState struct {
	// Status is Docker's word for it: created, running, restarting, exited, dead.
	Status     string
	Running    bool
	Restarting bool
	ExitCode   int
	Pid        int
	StartedAt  time.Time
}

type hostConfigWire struct {
	Binds         []string `json:",omitempty"`
	NetworkMode   string   `json:",omitempty"`
	RestartPolicy struct {
		Name string `json:",omitempty"`
	}
}

type createWire struct {
	Image      string
	User       string            `json:",omitempty"`
	Entrypoint []string          `json:",omitempty"`
	Cmd        []string          `json:",omitempty"`
	Env        []string          `json:",omitempty"`
	Labels     map[string]string `json:",omitempty"`
	StopSignal string            `json:",omitempty"`
	HostConfig hostConfigWire
}

// ContainerCreate creates a stopped container and returns its ID. A name that
// is already taken is an error matching ErrConflict.
func (c *Client) ContainerCreate(ctx context.Context, name string, cfg ContainerConfig) (string, error) {
	if err := checkName("container name", name); err != nil {
		return "", err
	}
	if err := checkRef(cfg.Image); err != nil {
		return "", err
	}
	body := createWire{
		Image: cfg.Image, User: cfg.User, Entrypoint: cfg.Entrypoint, Cmd: cfg.Cmd,
		Env: cfg.Env, Labels: cfg.Labels, StopSignal: cfg.StopSignal,
	}
	body.HostConfig.Binds = cfg.Binds
	body.HostConfig.NetworkMode = cfg.NetworkMode
	body.HostConfig.RestartPolicy.Name = cfg.RestartPolicy

	var out struct {
		ID string `json:"Id"`
	}
	q := url.Values{"name": {name}}
	if err := c.post(ctx, request{op: "create container " + name, path: "/containers/create", query: q}, body, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("docker: create container %s: the Engine returned no id", name)
	}
	return out.ID, nil
}

// ContainerStart starts a container. Starting a running container is not an error.
func (c *Client) ContainerStart(ctx context.Context, id string) error {
	if err := checkName("container", id); err != nil {
		return err
	}
	return c.call(ctx, request{
		op: "start container " + id, method: http.MethodPost, path: "/containers/" + id + "/start",
		okStatus: []int{http.StatusNotModified},
	}, nil)
}

// ContainerStop stops a container, sending the image's stop signal and killing
// it after timeout. Stopping a stopped container is not an error.
func (c *Client) ContainerStop(ctx context.Context, id string, timeout time.Duration) error {
	if err := checkName("container", id); err != nil {
		return err
	}
	q := url.Values{"t": {strconv.Itoa(int(timeout / time.Second))}}
	return c.call(ctx, request{
		op: "stop container " + id, method: http.MethodPost, path: "/containers/" + id + "/stop", query: q,
		okStatus: []int{http.StatusNotModified},
	}, nil)
}

// ContainerRemove removes a container and its anonymous volumes. With force a
// running container is killed first.
func (c *Client) ContainerRemove(ctx context.Context, id string, force bool) error {
	if err := checkName("container", id); err != nil {
		return err
	}
	q := url.Values{"v": {"1"}}
	if force {
		q.Set("force", "1")
	}
	return c.call(ctx, request{op: "remove container " + id, method: http.MethodDelete, path: "/containers/" + id, query: q}, nil)
}

// ContainerKill sends a signal to the container's main process. This is how
// services are reloaded (risk 9): the signal goes to PID 1 of the container,
// not to a shell started by exec that may not forward it.
func (c *Client) ContainerKill(ctx context.Context, id, signal string) error {
	if err := checkName("container", id); err != nil {
		return err
	}
	if err := checkName("signal", signal); err != nil {
		return err
	}
	q := url.Values{"signal": {signal}}
	return c.call(ctx, request{op: "signal container " + id, method: http.MethodPost, path: "/containers/" + id + "/kill", query: q}, nil)
}

// ContainerWait blocks until the container stops and returns its exit code.
func (c *Client) ContainerWait(ctx context.Context, id string) (int, error) {
	if err := checkName("container", id); err != nil {
		return 0, err
	}
	var out struct {
		StatusCode int
		Error      *struct{ Message string }
	}
	q := url.Values{"condition": {"not-running"}}
	err := c.call(ctx, request{op: "wait for container " + id, method: http.MethodPost, path: "/containers/" + id + "/wait", query: q}, &out)
	if err != nil {
		return 0, err
	}
	if out.Error != nil && out.Error.Message != "" {
		return out.StatusCode, fmt.Errorf("docker: wait for container %s: %s", id, out.Error.Message)
	}
	return out.StatusCode, nil
}

// ContainerInspect reads a container by name or ID, or returns an error
// matching ErrNotFound.
func (c *Client) ContainerInspect(ctx context.Context, id string) (Container, error) {
	if err := checkName("container", id); err != nil {
		return Container{}, err
	}
	var raw struct {
		ID     string `json:"Id"`
		Name   string
		Image  string
		Config struct {
			Labels map[string]string
		}
		State struct {
			Status     string
			Running    bool
			Restarting bool
			ExitCode   int
			Pid        int
			StartedAt  string
		}
	}
	if err := c.call(ctx, request{op: "inspect container " + id, method: http.MethodGet, path: "/containers/" + id + "/json"}, &raw); err != nil {
		return Container{}, err
	}
	out := Container{
		ID: raw.ID, Name: trimSlash(raw.Name), ImageID: raw.Image, Labels: raw.Config.Labels,
		State: ContainerState{
			Status: raw.State.Status, Running: raw.State.Running, Restarting: raw.State.Restarting,
			ExitCode: raw.State.ExitCode, Pid: raw.State.Pid,
		},
	}
	// A never-started container reports the zero time as a string; a parse
	// failure leaves StartedAt zero, which says the same thing.
	if t, err := time.Parse(time.RFC3339Nano, raw.State.StartedAt); err == nil && t.Year() > 1 {
		out.State.StartedAt = t
	}
	return out, nil
}

func trimSlash(name string) string {
	if len(name) > 0 && name[0] == '/' {
		return name[1:]
	}
	return name
}

// LogOptions selects what ContainerLogs returns.
type LogOptions struct {
	// Tail is how many lines from the end to start with; zero means all.
	Tail int
	// Follow keeps the stream open until the context ends or the container stops.
	Follow bool
}

// ContainerLogs copies a container's output to stdout and stderr until the
// stream ends or ctx is done.
func (c *Client) ContainerLogs(ctx context.Context, id string, opts LogOptions, stdout, stderr io.Writer) error {
	if err := checkName("container", id); err != nil {
		return err
	}
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}}
	if opts.Tail > 0 {
		q.Set("tail", strconv.Itoa(opts.Tail))
	}
	if opts.Follow {
		q.Set("follow", "1")
	}
	resp, err := c.do(ctx, request{op: "logs of container " + id, method: http.MethodGet, path: "/containers/" + id + "/logs", query: q})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return demux(resp.Body, stdout, stderr)
}

// ExecOptions describes a command to run inside a running container.
type ExecOptions struct {
	// Cmd is an argument vector. It is never joined into a shell line.
	Cmd  []string
	User string
	Env  []string
}

// ExecResult is a finished command's exit code and captured output.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	// Truncated reports that a stream exceeded the capture limit and was cut.
	Truncated bool
}

// Output is stdout followed by stderr, the form a validator message is shown in.
func (r ExecResult) Output() string {
	return string(r.Stdout) + string(r.Stderr)
}

// Exec runs a command in a running container and waits for it. A non-zero exit
// is reported in the result, not as an error: the caller is usually a validator
// that wants the output precisely when the command fails.
func (c *Client) Exec(ctx context.Context, id string, opts ExecOptions) (ExecResult, error) {
	if err := checkName("container", id); err != nil {
		return ExecResult{}, err
	}
	if len(opts.Cmd) == 0 {
		return ExecResult{}, fmt.Errorf("docker: exec in %s: command is empty", id)
	}
	op := "exec in " + id

	create := struct {
		AttachStdout bool
		AttachStderr bool
		Cmd          []string
		User         string   `json:",omitempty"`
		Env          []string `json:",omitempty"`
	}{true, true, opts.Cmd, opts.User, opts.Env}
	var created struct {
		ID string `json:"Id"`
	}
	if err := c.post(ctx, request{op: op, path: "/containers/" + id + "/exec"}, create, &created); err != nil {
		return ExecResult{}, err
	}
	if err := checkName("exec id", created.ID); err != nil {
		return ExecResult{}, err
	}

	// A plain POST rather than a hijacked connection: with no stdin to send,
	// the Engine streams the output as the response body and closes it when
	// the command exits.
	resp, err := c.do(ctx, request{
		op: op, method: http.MethodPost, path: "/exec/" + created.ID + "/start",
		body: strings.NewReader(`{"Detach":false,"Tty":false}`), contentType: "application/json",
	})
	if err != nil {
		return ExecResult{}, err
	}
	stdout := &capBuffer{limit: maxExecOutput}
	stderr := &capBuffer{limit: maxExecOutput}
	err = demux(resp.Body, stdout, stderr)
	_ = resp.Body.Close()
	if err != nil {
		return ExecResult{}, fmt.Errorf("docker: %s: %w", op, err)
	}

	var state struct {
		Running  bool
		ExitCode int
	}
	if err := c.call(ctx, request{op: op, method: http.MethodGet, path: "/exec/" + created.ID + "/json"}, &state); err != nil {
		return ExecResult{}, err
	}
	if state.Running {
		return ExecResult{}, fmt.Errorf("docker: %s: output ended but the command is still running", op)
	}
	return ExecResult{
		ExitCode: state.ExitCode, Stdout: stdout.buf, Stderr: stderr.buf,
		Truncated: stdout.truncated || stderr.truncated,
	}, nil
}

// RunOnce creates a container, runs it to completion, captures its output and
// removes it. It is how a validator runs when the long-lived container is not
// up: the same image and mounts, a different command.
func (c *Client) RunOnce(ctx context.Context, name string, cfg ContainerConfig) (ExecResult, error) {
	cfg.RestartPolicy = ""
	id, err := c.ContainerCreate(ctx, name, cfg)
	if err != nil {
		return ExecResult{}, err
	}
	// Removal must happen even when ctx is already canceled, or every aborted
	// validation would leave a stopped container behind.
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = c.ContainerRemove(rmCtx, id, true)
	}()

	if err := c.ContainerStart(ctx, id); err != nil {
		return ExecResult{}, err
	}
	code, err := c.ContainerWait(ctx, id)
	if err != nil {
		return ExecResult{}, err
	}
	stdout := &capBuffer{limit: maxExecOutput}
	stderr := &capBuffer{limit: maxExecOutput}
	if err := c.ContainerLogs(ctx, id, LogOptions{}, stdout, stderr); err != nil {
		return ExecResult{}, err
	}
	return ExecResult{
		ExitCode: code, Stdout: stdout.buf, Stderr: stderr.buf,
		Truncated: stdout.truncated || stderr.truncated,
	}, nil
}
