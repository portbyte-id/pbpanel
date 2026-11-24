package appsec

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ExecRunner runs commands on this host with exec.CommandContext. It names the
// executable and an argument vector, never a shell string.
type ExecRunner struct{}

// Run implements Runner. It returns the command's combined output whether it
// succeeded or not: the vendor's scripts print their explanation and their
// verdict on the same streams, and every caller needs that text either way.
func (ExecRunner) Run(ctx context.Context, c Command) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	if c.Dir != "" {
		cmd.Dir = c.Dir
	}
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil {
		if msg := strings.TrimSpace(buf.String()); msg != "" {
			return buf.Bytes(), fmt.Errorf("appsec: %s: %w", c.Name, err)
		}
		return buf.Bytes(), fmt.Errorf("appsec: %s %s: %w", c.Name, strings.Join(c.Args, " "), err)
	}
	return buf.Bytes(), nil
}
