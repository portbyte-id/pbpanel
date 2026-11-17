package apps

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Defaults every program file carries. They are the same for every app so a file
// reads the same whoever wrote the app; none is operator-tunable yet.
const (
	startSecs      = 3
	startRetries   = 5
	stopWaitSecs   = 10
	logMaxBytes    = "10MB"
	logBackups     = 3
	fileHeader     = "; Managed by Server Panel. Edit the app in the panel: this file is rewritten from it.\n"
	programSection = "program:"
)

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@+=:,./-]+$`)

// quote quotes one command-line word the way supervisord's command splitter
// reads it: a word of safe characters is left alone, anything else is put in
// single quotes.
func quote(s string) string {
	if s != "" && shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// commandLine builds the program's command. nodeBin is the absolute node path
// for a Node app and ignored otherwise.
func commandLine(a App, nodeBin string) string {
	words := []string{}
	if a.Type == TypeNode {
		words = append(words, quote(nodeBin))
	}
	words = append(words, quote(a.Command))
	for _, arg := range a.Args {
		words = append(words, quote(arg))
	}
	return strings.Join(words, " ")
}

// environment renders the environment= value: KEY="value" pairs in key order.
// PORT is added from the app's port unless the operator set it, which is how an
// app that reads PORT learns where to listen.
func environment(a App) string {
	env := map[string]string{}
	for k, v := range a.Env {
		env[k] = v
	}
	if a.Port != nil {
		if _, ok := env["PORT"]; !ok {
			env["PORT"] = strconv.Itoa(*a.Port)
		}
	}
	parts := make([]string, 0, len(env))
	for _, k := range sortedKeys(env) {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, env[k]))
	}
	return strings.Join(parts, ",")
}

func autorestart(r Restart) string {
	switch r {
	case RestartNever:
		return "false"
	case RestartOnFailure:
		return "unexpected"
	default:
		return "true"
	}
}

// Render builds the program file of an app. The same input always gives the same
// bytes, which is what makes the golden tests and the config history meaningful.
func Render(a App, p Paths, nodeBin string) []byte {
	var b strings.Builder
	b.WriteString(fileHeader)
	fmt.Fprintf(&b, "[%s%s]\n", programSection, a.Name)
	fmt.Fprintf(&b, "command=%s\n", commandLine(a, nodeBin))
	fmt.Fprintf(&b, "directory=%s\n", a.WorkDir)
	fmt.Fprintf(&b, "user=%s\n", a.User)
	if env := environment(a); env != "" {
		fmt.Fprintf(&b, "environment=%s\n", env)
	}
	fmt.Fprintf(&b, "autostart=%t\n", a.Autostart)
	fmt.Fprintf(&b, "autorestart=%s\n", autorestart(a.Restart))
	fmt.Fprintf(&b, "startsecs=%d\n", startSecs)
	fmt.Fprintf(&b, "startretries=%d\n", startRetries)
	b.WriteString("stopsignal=TERM\n")
	fmt.Fprintf(&b, "stopwaitsecs=%d\n", stopWaitSecs)
	fmt.Fprintf(&b, "stdout_logfile=%s\n", p.LogFile(a.Name))
	fmt.Fprintf(&b, "stdout_logfile_maxbytes=%s\n", logMaxBytes)
	fmt.Fprintf(&b, "stdout_logfile_backups=%d\n", logBackups)
	b.WriteString("redirect_stderr=true\n")
	return []byte(b.String())
}

// knownKeys are the program keys Render writes. CheckProgramFile accepts exactly
// these, so a file that drifted from what the panel renders is reported before
// supervisord is asked to read it.
var knownKeys = map[string]bool{
	"command": true, "directory": true, "user": true, "environment": true,
	"autostart": true, "autorestart": true, "startsecs": true, "startretries": true,
	"stopsignal": true, "stopwaitsecs": true, "stdout_logfile": true,
	"stdout_logfile_maxbytes": true, "stdout_logfile_backups": true, "redirect_stderr": true,
}

// CheckProgramFile parses a rendered program file and reports the first thing
// wrong with it: the step 5.4 validator that runs before reloadConfig. It checks
// the shape (one [program:x] section, known keys, a command, an absolute
// directory), not whether the program can start; supervisord's own parse in
// reloadConfig is the second check.
func CheckProgramFile(content []byte) error {
	var section string
	keys := map[string]bool{}
	for i, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return fmt.Errorf("line %d: unterminated section header", i+1)
			}
			if section != "" {
				return fmt.Errorf("line %d: a program file holds exactly one section", i+1)
			}
			section = line[1 : len(line)-1]
			if !strings.HasPrefix(section, programSection) || !nameRe.MatchString(strings.TrimPrefix(section, programSection)) {
				return fmt.Errorf("line %d: %q is not a [program:name] section", i+1, section)
			}
			continue
		}
		if section == "" {
			return fmt.Errorf("line %d: a key before the [program:name] section", i+1)
		}
		k, _, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("line %d: expected key=value", i+1)
		}
		k = strings.TrimSpace(k)
		if !knownKeys[k] {
			return fmt.Errorf("line %d: unknown key %q", i+1, k)
		}
		keys[k] = true
	}
	if section == "" {
		return fmt.Errorf("no [program:name] section")
	}
	for _, need := range []string{"command", "directory", "user"} {
		if !keys[need] {
			return fmt.Errorf("[%s] has no %s", section, need)
		}
	}
	return nil
}
