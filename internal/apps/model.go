package apps

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Type is what kind of program an app is. It decides how Command is read.
type Type string

// The app types of development plan step 5.4.
const (
	// TypeGo is a compiled binary: Command is its absolute path.
	TypeGo Type = "go"
	// TypeNode is a Node.js app: Command is the entry script, run by the node
	// binary of NodeVersion.
	TypeNode Type = "node"
	// TypeCommand is any other program: Command is its absolute path.
	TypeCommand Type = "command"
)

// Restart is what supervisord does when the program exits.
type Restart string

// Restart policies.
const (
	RestartAlways    Restart = "always"
	RestartOnFailure Restart = "on-failure"
	RestartNever     Restart = "never"
)

// DefaultUser is the account apps run as when none is given (decision 23).
const DefaultUser = "www"

// App is one supervised program.
type App struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Type        Type              `json:"type"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	WorkDir     string            `json:"workdir"`
	Env         map[string]string `json:"env"`
	User        string            `json:"user"`
	NodeVersion string            `json:"node_version"`
	Autostart   bool              `json:"autostart"`
	Restart     Restart           `json:"restart"`
	Port        *int              `json:"port"`
	CreatedAt   string            `json:"created_at"`
}

// Input is what an operator submits to create or replace an app. Optional fields
// take their documented default in Normalize.
type Input struct {
	Name        string            `json:"name"`
	Type        Type              `json:"type"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	WorkDir     string            `json:"workdir"`
	Env         map[string]string `json:"env"`
	User        string            `json:"user"`
	NodeVersion string            `json:"node_version"`
	Autostart   *bool             `json:"autostart"`
	Restart     Restart           `json:"restart"`
	Port        *int              `json:"port"`
}

// ValidationError names the field an operator has to fix.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Msg }

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// Errors the manager returns.
var (
	ErrNotFound     = errors.New("apps: no such app")
	ErrExists       = errors.New("apps: an app with that name already exists")
	ErrPortInUse    = errors.New("apps: another app already uses that port")
	ErrNodeMissing  = errors.New("apps: that Node.js version is not installed")
	ErrNoSupervisor = errors.New("apps: supervisord is not running")
)

var (
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	envKeyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	userRe    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	nodeVerRe = regexp.MustCompile(`^[0-9]{1,2}(\.[0-9]{1,3}){0,2}$`)
)

const (
	maxArgs     = 64
	maxEnv      = 64
	maxTextLen  = 4096
	maxEnvValue = 4096
)

// reserved are names a program must not take: they would collide with the
// supervisor's own vocabulary or read as the panel itself.
var reserved = map[string]bool{"panel": true, "supervisord": true, "all": true}

// Normalize applies defaults and validates the input, returning the App to
// store. A problem is always a *ValidationError.
//
// The character rules are strict on purpose. The program file is an INI file that
// supervisord interpolates (`%(…)s`), and the command line is split by shell
// rules, so a value is refused here rather than escaped to a form nobody has
// verified: no `%`, no newline, no NUL in anything that reaches the file, and no
// double quote in an environment value.
func (in Input) Normalize(defaultWorkDir func(name string) string) (App, error) {
	a := App{
		Name: in.Name, Type: in.Type, Command: in.Command, Args: in.Args,
		WorkDir: in.WorkDir, Env: in.Env, User: in.User, NodeVersion: in.NodeVersion,
		Restart: in.Restart, Port: in.Port, Autostart: true,
	}
	if in.Autostart != nil {
		a.Autostart = *in.Autostart
	}
	if !nameRe.MatchString(a.Name) {
		return a, invalid("name", "use 1–32 lowercase letters, digits, - or _, starting with a letter")
	}
	if reserved[a.Name] {
		return a, invalid("name", "%q is reserved", a.Name)
	}
	switch a.Type {
	case TypeGo, TypeNode, TypeCommand:
	default:
		return a, invalid("type", "must be go, node or command")
	}

	if err := checkText("command", a.Command); err != nil {
		return a, err
	}
	switch a.Type {
	case TypeGo, TypeCommand:
		if !path.IsAbs(a.Command) || path.Clean(a.Command) != a.Command {
			return a, invalid("command", "must be a clean absolute path to the program")
		}
	case TypeNode:
		if strings.HasPrefix(a.Command, "-") {
			return a, invalid("command", "must be the entry script, not an option")
		}
	}

	if a.Args == nil {
		a.Args = []string{}
	}
	if len(a.Args) > maxArgs {
		return a, invalid("args", "at most %d arguments", maxArgs)
	}
	for i, arg := range a.Args {
		if err := checkText(fmt.Sprintf("args[%d]", i), arg); err != nil {
			return a, err
		}
	}

	if a.WorkDir == "" {
		a.WorkDir = defaultWorkDir(a.Name)
	}
	if !path.IsAbs(a.WorkDir) || path.Clean(a.WorkDir) != a.WorkDir {
		return a, invalid("workdir", "must be a clean absolute path")
	}
	if err := checkText("workdir", a.WorkDir); err != nil {
		return a, err
	}

	if a.Env == nil {
		a.Env = map[string]string{}
	}
	if len(a.Env) > maxEnv {
		return a, invalid("env", "at most %d variables", maxEnv)
	}
	for k, v := range a.Env {
		if !envKeyRe.MatchString(k) {
			return a, invalid("env", "%q is not a valid variable name", k)
		}
		if len(v) > maxEnvValue {
			return a, invalid("env", "the value of %s is too long", k)
		}
		if err := checkText("env", v); err != nil {
			return a, err
		}
		if strings.Contains(v, `"`) {
			return a, invalid("env", "the value of %s contains a double quote, which the program file cannot carry", k)
		}
	}

	if a.User == "" {
		a.User = DefaultUser
	}
	if !userRe.MatchString(a.User) {
		return a, invalid("user", "is not a valid account name")
	}

	if a.Type == TypeNode {
		if !nodeVerRe.MatchString(a.NodeVersion) {
			return a, invalid("node_version", "use a version such as 22, 22.11 or 22.11.0")
		}
	} else {
		a.NodeVersion = ""
	}

	switch a.Restart {
	case "":
		a.Restart = RestartAlways
	case RestartAlways, RestartOnFailure, RestartNever:
	default:
		return a, invalid("restart", "must be always, on-failure or never")
	}

	if a.Port != nil && (*a.Port < 1 || *a.Port > 65535) {
		return a, invalid("port", "must be between 1 and 65535")
	}
	return a, nil
}

// checkText refuses the characters the program file cannot carry safely.
func checkText(field, s string) error {
	if s == "" && field == "command" {
		return invalid(field, "is required")
	}
	if len(s) > maxTextLen {
		return invalid(field, "is longer than %d bytes", maxTextLen)
	}
	for _, r := range s {
		switch r {
		case '%':
			return invalid(field, "must not contain %%; supervisord would read it as a substitution")
		case 0, '\n', '\r':
			return invalid(field, "must not contain a newline or NUL")
		}
	}
	return nil
}

// ProxyURL is where the app listens, for the "create a proxy site" shortcut. It
// is empty when the app has no port.
func (a App) ProxyURL() string {
	if a.Port == nil {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", *a.Port)
}

// sortedKeys returns a map's keys in order, so a rendered file is stable.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
