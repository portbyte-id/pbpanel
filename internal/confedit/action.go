package confedit

import "context"

// Funcs is a Service assembled from functions.
//
// It exists for a service whose lifecycle is not a daemon to reload: the
// open-appsec agent, for instance, has no reload signal — it is told to read a
// policy with a command of its own, which is this pipeline's "apply" step. A
// struct of functions is the plainest way to say that without teaching this
// package anything about a particular product.
//
// The three steps keep the meaning they have for every other service, and the
// pipeline's guarantees come with them: Validate runs with every file of the
// change already on disk and before anything is applied; Apply runs after every
// Validate in the change has passed; and a failure at either step rolls the
// files back and runs Apply again on the restored config.
type Funcs struct {
	// ServiceName identifies the service in errors and orders its apply lock.
	ServiceName string
	// Validate checks the configuration on disk. It returns the rejecter's
	// output verbatim, for the UI, and a non-nil error.
	ValidateFunc func(ctx context.Context) (string, error)
	// Apply makes the running service take the configuration on disk.
	ApplyFunc func(ctx context.Context) error
	// CheckFunc reports whether the service is healthy after an apply. Nil
	// accepts an apply that returned no error.
	CheckFunc func(ctx context.Context) error
}

// Name implements Service.
func (f *Funcs) Name() string { return f.ServiceName }

// Validate implements Service.
func (f *Funcs) Validate(ctx context.Context) (string, error) {
	if f.ValidateFunc == nil {
		return "", nil
	}
	return f.ValidateFunc(ctx)
}

// Reload implements Service: for a function-backed service, making the running
// service read the config *is* the apply.
func (f *Funcs) Reload(ctx context.Context) error {
	if f.ApplyFunc == nil {
		return nil
	}
	return f.ApplyFunc(ctx)
}

// Healthy implements Service.
func (f *Funcs) Healthy(ctx context.Context) error {
	if f.CheckFunc == nil {
		return nil
	}
	return f.CheckFunc(ctx)
}

var _ Service = (*Funcs)(nil)
