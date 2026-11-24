package confedit

import (
	"context"
	"errors"
	"testing"
)

// TestFuncsIsAService: a function-backed service takes part in the pipeline like
// any other, which is what lets a product that applies rather than reloads use
// the same all-or-nothing change.
func TestFuncsIsAService(t *testing.T) {
	e := newEnv(t)
	p := e.seed(t, "policy.yaml", "old")
	var validated, applied string

	f := &Funcs{
		ServiceName: "agent",
		ValidateFunc: func(context.Context) (string, error) {
			// The file is on disk before a validator runs, as for any service.
			validated = read(t, p)
			return "", nil
		},
		ApplyFunc: func(context.Context) error { applied = read(t, p); return nil },
	}
	if _, err := e.ed.Apply(context.Background(), Change{Files: []File{{Path: p, Content: []byte("new")}}, Services: []Service{f}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if validated != "new" || applied != "new" {
		t.Errorf("validate saw %q, apply saw %q; both want the new content", validated, applied)
	}
}

// A rejecting function rolls the file back exactly as a rejecting nginx does.
func TestFuncsRollback(t *testing.T) {
	e := newEnv(t)
	p := e.seed(t, "policy.yaml", "old")
	f := &Funcs{
		ServiceName:  "agent",
		ValidateFunc: func(context.Context) (string, error) { return "the agent said no", errors.New("rejected") },
	}
	_, err := e.ed.Apply(context.Background(), Change{Files: []File{{Path: p, Content: []byte("new")}}, Services: []Service{f}})
	ae := applyErr(t, err)
	if ae.Stage != StageValidate || ae.Service != "agent" || ae.Output != "the agent said no" {
		t.Errorf("error = %+v, want a validate failure naming the agent with its output", ae)
	}
	if read(t, p) != "old" {
		t.Errorf("the file = %q, want the previous content", read(t, p))
	}
	if n := e.pendingJournals(t); n != 0 {
		t.Errorf("%d journal(s) left after a clean rollback", n)
	}
}

// A service with no functions does nothing, which is how a caller says "this
// apply has no non-nginx step" without a nil interface.
func TestFuncsZeroValue(t *testing.T) {
	var f Funcs
	if f.Name() != "" {
		t.Errorf("Name = %q, want empty", f.Name())
	}
	if out, err := f.Validate(context.Background()); out != "" || err != nil {
		t.Errorf("Validate = %q, %v", out, err)
	}
	if err := f.Reload(context.Background()); err != nil {
		t.Errorf("Reload = %v", err)
	}
	if err := f.Healthy(context.Background()); err != nil {
		t.Errorf("Healthy = %v", err)
	}
}
