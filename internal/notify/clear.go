package notify

import "context"

// Clear records a resolved event for e.Key only when that key is open. A
// success that clears nothing must not add a row to the bell list, so the
// producers that report successes (jobs, cron, renewals) call Clear rather
// than Emit with Resolved set.
func (s *Service) Clear(ctx context.Context, e Event) {
	key := e.Key
	if key == "" {
		key = e.Kind
	}
	st, found, err := s.repo.getState(ctx, s.repo.st.DB(), key)
	if err != nil || !found || !st.Open {
		return
	}
	e.Resolved = true
	s.Emit(ctx, e)
}
