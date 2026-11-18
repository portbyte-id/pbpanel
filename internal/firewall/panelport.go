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
	return openPorts(ctx, cmd, PanelPortComment, []portSpec{{"tcp", port}}, ipv6)
}

// WebPortsComment tags the INPUT rules that keep HTTP, HTTPS and QUIC reachable while
// the managed ruleset is off.
const WebPortsComment = "panel:web"

// OpenWebPorts opens TCP 80, TCP 443 and UDP 443 from anywhere, like OpenPanelPort.
func OpenWebPorts(ctx context.Context, cmd Cmd, ipv6 bool) error {
	return openPorts(ctx, cmd, WebPortsComment, webPorts, ipv6)
}

// openPorts replaces every INPUT rule carrying comment with one ACCEPT rule per spec,
// in each family.
func openPorts(ctx context.Context, cmd Cmd, comment string, specs []portSpec, ipv6 bool) error {
	var errs []error
	for _, fc := range families(ipv6) {
		rules, err := cmd.Output(ctx, fc.bin, "-S", "INPUT")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for line := range strings.SplitSeq(string(rules), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 || fields[0] != "-A" || fields[1] != "INPUT" || !hasComment(fields, comment) {
				continue
			}
			if err := cmd.RunStdin(ctx, "", fc.bin, append([]string{"-D", "INPUT"}, fields[2:]...)...); err != nil {
				errs = append(errs, err)
			}
		}
		for _, sp := range specs {
			args := []string{"-I", "INPUT", "1", "-p", sp.proto, "-m", sp.proto, "--dport", strconv.Itoa(sp.port),
				"-m", "comment", "--comment", comment, "-j", "ACCEPT"}
			if err := cmd.RunStdin(ctx, "", fc.bin, args...); err != nil {
				errs = append(errs, err)
			}
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
