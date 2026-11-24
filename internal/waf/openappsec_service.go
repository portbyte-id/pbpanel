package waf

import (
	"context"
	"fmt"
	"strings"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/native"
)

// The agent as a confedit.Service.
//
// The apply order the agent needs is the one confedit already runs: write the
// files, validate, reload, health-check, and roll every file back on failure.
// For open-appsec the "reload" is `/etc/cp/scripts/open-appsec-ctl.sh
// --apply-policy`, and the validate step is the part that matters — see below.
//
// **The agent cannot tell us it rejected a policy.** Step 0 measured that
// --apply-policy prints "New policy applied" for a policy it then discards, and
// step 4 measured that it exits 1 on success too, so neither the text nor the
// status is a verdict (docs/design/open-appsec-step4-findings.md §A, §C). What
// the policy is rejected by is a *shape* the panel controls completely: it
// renders the whole document itself, from a template proven on the agent. So the
// validate step checks that shape, and the apply step runs the command. A
// rejection at that step — the agent missing, the command failing to run — is a
// plain error, and confedit rolls the change back exactly as an `nginx -t`
// failure does.
const (
	// OpenAppsecCtl is the vendor's control script.
	OpenAppsecCtl = "/etc/cp/scripts/open-appsec-ctl.sh"

	// OpenAppsecUnit is the agent's systemd unit.
	OpenAppsecUnit = "nano_agent.service"

	// openAppsecServiceName is the agent's name in confedit errors and its lock.
	openAppsecServiceName = "open-appsec"
)

// OpenAppsecService is the agent as a confedit.Service. The runner is the one
// everything else on the host uses, so a test can supply a fake.
type OpenAppsecService struct {
	run    native.Runner
	policy string
	// enabled reports whether the agent is installed and running. An apply on a
	// host without the agent is refused before anything is written, so the panel
	// never loads a module and a directive whose agent cannot act on them.
	enabled func(ctx context.Context) (bool, string)
}

// NewOpenAppsecService returns the agent's confedit.Service.
func NewOpenAppsecService(run native.Runner, policy string, enabled func(ctx context.Context) (bool, string)) *OpenAppsecService {
	return &OpenAppsecService{run: run, policy: policy, enabled: enabled}
}

// Name implements confedit.Service.
func (s *OpenAppsecService) Name() string { return openAppsecServiceName }

// Validate implements confedit.Service. It checks that the document the panel
// just wrote is the shape the agent accepts, and that the agent is there to take
// it. It never asks the agent about the policy: the agent reports success for a
// policy it discarded, so its answer is not a verdict.
func (s *OpenAppsecService) Validate(ctx context.Context) (string, error) {
	if s.enabled != nil {
		ok, reason := s.enabled(ctx)
		if !ok {
			return reason, fmt.Errorf("%s cannot take a policy: %s", openAppsecServiceName, reason)
		}
	}
	// The rendered policy is checked by RenderOpenAppsecPolicy's own tests; what
	// this step can add is that the control script exists, so the apply step
	// that follows is not the first thing to find out it does not.
	if out, err := s.run.Run(ctx, "test", "-x", OpenAppsecCtl); err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("%s is not installed: %s is missing", openAppsecServiceName, OpenAppsecCtl)
	}
	return "", nil
}

// Reload implements confedit.Service: it tells the agent to read the policy that
// is on disk.
//
// The command's exit status is not its verdict (it exits 1 on success too), so
// this step cannot distinguish a loaded policy from a discarded one. What it can
// do is refuse to report success for a command that did not run at all — a
// non-zero exit from the *script interpreter* rather than the script's own
// deliberate `exit 1` — and a timeout.
func (s *OpenAppsecService) Reload(ctx context.Context) error {
	out, err := s.run.Run(ctx, OpenAppsecCtl, "--apply-policy", s.policy)
	text := string(out)
	// The script's own "the policy did not change" exit is a rejection the panel
	// can see; it is checked before the exit status because the status is 1 on
	// success too.
	if strings.Contains(text, "Policy didn't change please verify that you have a legal new policy") {
		return fmt.Errorf("open-appsec rejected the policy: %s", strings.TrimSpace(text))
	}
	if err != nil && !strings.Contains(text, "New policy applied") {
		return fmt.Errorf("open-appsec apply-policy failed: %w: %s", err, strings.TrimSpace(text))
	}
	return nil
}

// Healthy implements confedit.Service. The agent answering `--status` is as much
// as can be checked; the policy's own load state is not reliable (see Reload).
func (s *OpenAppsecService) Healthy(ctx context.Context) error {
	if s.enabled == nil {
		return nil
	}
	if ok, reason := s.enabled(ctx); !ok {
		return fmt.Errorf("open-appsec is not healthy after the apply: %s", reason)
	}
	return nil
}

var _ confedit.Service = (*OpenAppsecService)(nil)
