package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// euid reports the effective user id. It is a variable so the root check can be
// exercised on any OS; the real syscall path is covered in root_unix_test.go.
var euid = os.Geteuid

// command is one subcommand. Later steps replace run with a real implementation;
// the parser, the root check and the usage rendering stay as they are.
type command struct {
	name    string
	summary string // one line, shown in the command list
	usage   string // full usage line, e.g. "panel set <key> <value>"
	minArgs int
	maxArgs int // -1 means unbounded
	noRoot  bool
	run     func(ctx context.Context, args []string) error
}

// usageError is a bad invocation: no command, an unknown command, or the wrong
// number of arguments. It exits 2 and carries the usage text to print after the
// message.
type usageError struct {
	msg   string
	usage string
}

func (e *usageError) Error() string { return e.msg }

// selection is the result of parsing argv.
type selection struct {
	cmd  *command // nil when only top-level help was asked for
	args []string // arguments left for the command
	help bool     // print usage instead of running
}

// usageText returns what to print for this selection: the command's own usage
// line, or the full command list.
func (s selection) usageText(cmds []*command) string {
	if s.cmd == nil {
		return usageText(cmds)
	}
	return "usage: " + s.cmd.usage + "\n"
}

// selectCommand resolves argv to one command and its arguments. It is the whole
// parser: it touches no process state, so it is tested on its own.
func selectCommand(argv []string, cmds []*command) (selection, error) {
	if len(argv) == 0 {
		return selection{}, &usageError{msg: "no command given", usage: usageText(cmds)}
	}

	name := argv[0]
	if isHelpFlag(name) {
		return selection{help: true}, nil
	}
	if name == "--version" {
		name = "version"
		argv = []string{name}
	}

	cmd, consumed := lookup(argv, cmds)
	if cmd == nil {
		return selection{}, &usageError{
			msg:   fmt.Sprintf("unknown command %q", argv[0]),
			usage: usageText(cmds),
		}
	}

	var args []string
	if consumed < len(argv) {
		args = argv[consumed:]
	}
	// Help wins over arity: `panel set -h` asks for help, not for arguments.
	if len(args) > 0 && isHelpFlag(args[0]) {
		return selection{cmd: cmd, help: true}, nil
	}
	if len(args) < cmd.minArgs {
		return selection{}, &usageError{
			msg:   fmt.Sprintf("%s: missing arguments", cmd.name),
			usage: "usage: " + cmd.usage,
		}
	}
	if cmd.maxArgs >= 0 && len(args) > cmd.maxArgs {
		return selection{}, &usageError{
			msg:   fmt.Sprintf("%s: too many arguments", cmd.name),
			usage: "usage: " + cmd.usage,
		}
	}

	return selection{cmd: cmd, args: args}, nil
}

// run parses argv, dispatches to one command and maps the outcome to an exit
// code. main does nothing beyond calling it.
func run(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	return runWith(ctx, argv, commandSet(stdout), stdout, stderr)
}

// runWith is run over an explicit command list, so dispatch can be tested with a
// command whose run records what it was given.
func runWith(ctx context.Context, argv []string, cmds []*command, stdout, stderr io.Writer) int {
	sel, err := selectCommand(argv, cmds)
	if err != nil {
		printf(stderr, "panel: %v\n", err)
		if ue, ok := errors.AsType[*usageError](err); ok && ue.usage != "" {
			printf(stderr, "\n%s\n", ue.usage)
		}
		return exitCode(err)
	}

	if sel.help {
		printf(stdout, "%s", sel.usageText(cmds))
		return exitOK
	}

	// The one root check. `version` opts out through noRoot; help has already
	// returned, so it never reaches this.
	if !sel.cmd.noRoot {
		if uid := euid(); uid != 0 {
			printf(stderr,
				"panel: %s must run as root (effective uid is %d); re-run it with sudo\n",
				sel.cmd.name, uid)
			return exitError
		}
	}

	if err := sel.cmd.run(ctx, sel.args); err != nil {
		printf(stderr, "panel: %v\n", err)
		return exitCode(err)
	}
	return exitOK
}

// printf writes a diagnostic to the terminal. The write error is deliberately
// discarded: every call site is on the way out of the process, there is nothing
// left to report a broken stderr to, and the exit code already carries the
// outcome. It exists so errcheck does not have to be silenced per call.
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func exitCode(err error) int {
	if _, ok := errors.AsType[*usageError](err); ok {
		return exitUsage
	}
	return exitError
}

// lookup finds the command whose name is the longest prefix of argv, and reports
// how many argv words the name consumed. That is what makes the two-word names
// (`firewall restore`) work without special cases.
func lookup(argv []string, cmds []*command) (*command, int) {
	var best *command
	bestWords := 0
	for _, c := range cmds {
		words := strings.Fields(c.name)
		if len(words) == 0 || len(words) > len(argv) {
			continue
		}
		if !slices.Equal(argv[:len(words)], words) {
			continue
		}
		if len(words) > bestWords {
			best, bestWords = c, len(words)
		}
	}
	return best, bestWords
}

func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "--help"
}

func usageText(cmds []*command) string {
	var b strings.Builder
	b.WriteString("panel - single-server management panel\n")
	b.WriteString("\nusage:\n  panel <command> [args]\n\ncommands:\n")
	for _, c := range cmds {
		fmt.Fprintf(&b, "  %-32s %s\n", c.usage, c.summary)
	}
	fmt.Fprintf(&b, "  %-32s %s\n", "-h, --help", "print this help and exit")
	fmt.Fprintf(&b, "  %-32s %s\n", "--version", "print the version and exit")
	b.WriteString("\nEvery command except `version` and `--help` must run as root.\n")
	return b.String()
}
