package waf

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner answers commands by name, so the agent service can be driven
// without a host.
type fakeRunner struct {
	calls []string
	out   map[string]string
	err   map[string]error
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, key)
	for k, v := range r.out {
		if strings.HasPrefix(key, k) {
			return []byte(v), r.err[k]
		}
	}
	for k, err := range r.err {
		if strings.HasPrefix(key, k) {
			return nil, err
		}
	}
	return nil, nil
}

// The agent reports success for a policy it discarded, and exits 1 on success
// too, so the apply step must not read either as a verdict. What it must catch
// is a command that did not run at all.
func TestOpenAppsecServiceApply(t *testing.T) {
	ok := &fakeRunner{out: map[string]string{
		"/etc/cp/scripts/open-appsec-ctl.sh": "Applying new policy.\n.....\nNew policy applied.\n",
	}}
	// The vendor script's own success path ends in `exit 1`.
	ok.err = map[string]error{"/etc/cp/scripts/open-appsec-ctl.sh": errors.New("exit status 1")}
	s := NewOpenAppsecService(ok, filepath.Join(t.TempDir(), "local_policy.yaml"), nil)
	if err := s.Reload(context.Background()); err != nil {
		t.Errorf("a successful apply reported an error: %v", err)
	}

	missing := &fakeRunner{err: map[string]error{"/etc/cp/scripts/open-appsec-ctl.sh": errors.New("no such file")}}
	err := NewOpenAppsecService(missing, filepath.Join(t.TempDir(), "local_policy.yaml"), nil).Reload(context.Background())
	if err == nil {
		t.Fatal("an apply that never ran reported success")
	}
	if !strings.Contains(err.Error(), "apply-policy") {
		t.Errorf("the error does not name the command: %v", err)
	}
}

// The control script's own "did not change" exit is a rejection the panel can
// see, and it must be an error rather than a silent pass.
func TestOpenAppsecServiceRejectsAnUnchangedPolicy(t *testing.T) {
	r := &fakeRunner{
		out: map[string]string{"/etc/cp/scripts/open-appsec-ctl.sh": "Policy didn't change please verify that you have a legal new policy"},
		err: map[string]error{"/etc/cp/scripts/open-appsec-ctl.sh": errors.New("exit status 1")},
	}
	err := NewOpenAppsecService(r, filepath.Join(t.TempDir(), "local_policy.yaml"), nil).Reload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("err = %v, want a rejection", err)
	}
}

// Validate refuses before anything is applied when the agent is not there, so a
// host without it cannot end up with a module loaded and no agent to act on it.
func TestOpenAppsecServiceValidate(t *testing.T) {
	absent := NewOpenAppsecService(&fakeRunner{}, "/p.yaml", func(context.Context) (bool, string) {
		return false, "nano_agent.service is inactive"
	})
	if _, err := absent.Validate(context.Background()); err == nil {
		t.Error("a host with no running agent validated")
	}

	present := &fakeRunner{out: map[string]string{"test -x": ""}}
	s := NewOpenAppsecService(present, "/p.yaml", func(context.Context) (bool, string) { return true, "" })
	if _, err := s.Validate(context.Background()); err != nil {
		t.Errorf("a host with the agent refused: %v", err)
	}
	if err := s.Healthy(context.Background()); err != nil {
		t.Errorf("Healthy = %v", err)
	}
	if s.Name() != "open-appsec" {
		t.Errorf("Name = %q", s.Name())
	}
}

// A rollback of the first enable restores "no policy file". The agent must still
// be handed a file it can load: the all-inactive default, not a missing path.
func TestOpenAppsecReloadWritesDefaultPolicyWhenAbsent(t *testing.T) {
	policy := filepath.Join(t.TempDir(), "waf", "open-appsec", "local_policy.yaml")
	r := &fakeRunner{out: map[string]string{"/etc/cp/scripts/open-appsec-ctl.sh": "New policy applied.\n"}}
	s := NewOpenAppsecService(r, policy, nil)
	if err := s.Reload(context.Background()); err != nil {
		t.Fatalf("reload over an absent policy: %v", err)
	}
	got, err := os.ReadFile(policy)
	if err != nil {
		t.Fatalf("the default policy was not written: %v", err)
	}
	if !bytes.Equal(got, RenderOpenAppsecPolicy(nil)) {
		t.Errorf("the written policy is not the all-inactive default")
	}
}

// An existing policy is what the apply must load; the default must not replace it.
func TestOpenAppsecReloadKeepsExistingPolicy(t *testing.T) {
	policy := filepath.Join(t.TempDir(), "local_policy.yaml")
	mine := []byte("kept\n")
	if err := os.WriteFile(policy, mine, 0o644); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{out: map[string]string{"/etc/cp/scripts/open-appsec-ctl.sh": "New policy applied.\n"}}
	if err := NewOpenAppsecService(r, policy, nil).Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, _ := os.ReadFile(policy); !bytes.Equal(got, mine) {
		t.Errorf("an existing policy was overwritten: %q", got)
	}
}
