package firewall

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// PanelPortComment tags the one INPUT rule that keeps the panel port reachable while
// the managed ruleset is off. The rule lives in INPUT, not in the panel's chains, so
// Disable and a ruleset flush never remove it.
const PanelPortComment = "panel:open"

// OpenPanelPort makes port reachable from anywhere over TCP, in every family the host
// uses. It deletes any earlier PanelPortComment rule first, so a port change replaces
// the old rule and repeated calls leave exactly one rule per family.
//
// A fresh install starts with the managed firewall disabled, and many hosts reject
// new inbound connections by default. Without this rule the panel is unreachable
// until someone enables the firewall, and the UI that does that is behind the port.
func OpenPanelPort(ctx context.Context, cmd Cmd, port int, ipv6 bool) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("firewall: panel port %d is out of range", port)
	}
	spec := []string{"-p", "tcp", "-m", "tcp", "--dport", strconv.Itoa(port),
		"-m", "comment", "--comment", PanelPortComment, "-j", "ACCEPT"}
	var errs []error
	for _, fc := range families(ipv6) {
		rules, err := cmd.Output(ctx, fc.bin, "-S", "INPUT")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for line := range strings.SplitSeq(string(rules), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 || fields[0] != "-A" || fields[1] != "INPUT" || !hasComment(fields, PanelPortComment) {
				continue
			}
			if err := cmd.RunStdin(ctx, "", fc.bin, append([]string{"-D", "INPUT"}, fields[2:]...)...); err != nil {
				errs = append(errs, err)
			}
		}
		if err := cmd.RunStdin(ctx, "", fc.bin, append([]string{"-I", "INPUT", "1"}, spec...)...); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// hasComment reports whether the rule's argument vector carries the given comment.
func hasComment(fields []string, comment string) bool {
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "--comment" && fields[i+1] == comment {
			return true
		}
	}
	return false
}
