package update

import "context"

// Service is what the HTTP layer serves: the cached release check and the
// detached upgrade. It is a thin pairing so the HTTP tests can substitute one
// fake for both halves.
type Service struct {
	Check   *Checker
	Upgrade *Starter
}

// Info is the update state, refreshed from GitHub when force is set.
func (s *Service) Info(ctx context.Context, force bool) Info {
	return s.Check.Info(ctx, force)
}

// Status is the most recent upgrade, or the idle state when none has run.
func (s *Service) Status() (Status, error) {
	return s.Upgrade.Status()
}

// Start queues an upgrade to tag and returns its queued status.
func (s *Service) Start(ctx context.Context, tag string) (Status, error) {
	return s.Upgrade.Start(ctx, tag)
}
