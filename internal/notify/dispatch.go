package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Delivery policy (§4.3): a send times out after 15 s; a failed send is
// retried with backoff 30 s, 2 m, 10 m, 30 m; the fifth failure marks the
// delivery failed, and D10 raises a bell-only event.
const (
	sendTimeout = 15 * time.Second
	maxAttempts = 5
	drainBatch  = 20
	idlePoll    = 30 * time.Second
)

var retryBackoff = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 2 * time.Hour}

// DispatcherOptions configures the sender side of notify.
type DispatcherOptions struct {
	Service  *Service
	Host     string // label in messages (notify.label, or the host name)
	PanelURL string // absolute panel base for links, "" for no links
	Logger   *slog.Logger
	Now      func() time.Time
}

// Dispatcher sends queued deliveries. It runs on one goroutine, so a slow
// channel delays only the rows behind it, never a producer.
type Dispatcher struct {
	svc      *Service
	repo     *Repo
	client   *http.Client
	host     string
	panelURL string
	logger   *slog.Logger
	now      func() time.Time

	// telegramBase and smtpTLS are overridable for tests.
	telegramBase string
	smtpTLS      *tls.Config
}

// NewDispatcher builds the dispatcher over a Service.
func NewDispatcher(opts DispatcherOptions) *Dispatcher {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Dispatcher{
		svc:          opts.Service,
		repo:         opts.Service.repo,
		client:       newOutboundClient(),
		host:         opts.Host,
		panelURL:     opts.PanelURL,
		logger:       logger,
		now:          now,
		telegramBase: "https://api.telegram.org", // (assumed, see senders.go)
	}
}

// Run drains the queue, then waits for a wake-up from the engine or the poll
// interval (which picks up retries that came due). It returns when ctx ends.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(idlePoll)
	defer ticker.Stop()
	for {
		d.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-d.svc.Kick():
		case <-ticker.C:
		}
	}
}

// drain delivers due rows until none are left.
func (d *Dispatcher) drain(ctx context.Context) {
	for {
		due, err := d.repo.dueDeliveries(ctx, d.now(), drainBatch)
		if err != nil {
			d.logger.Error("notify: could not read the queue", "err", err)
			return
		}
		for _, row := range due {
			if ctx.Err() != nil {
				return
			}
			d.deliver(ctx, row)
		}
		if len(due) < drainBatch {
			return
		}
	}
}

// deliver sends one row and records the outcome.
func (d *Dispatcher) deliver(ctx context.Context, row delivery) {
	now := d.now()
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	ev, err := d.repo.getEvent(sctx, d.repo.st.DB(), row.EventID)
	if err != nil {
		d.fail(ctx, row, nil, permanent("event %d missing: %v", row.EventID, err))
		return
	}
	ch, err := d.repo.channelByID(sctx, d.repo.st.DB(), row.ChannelID)
	if err != nil {
		d.fail(ctx, row, nil, permanent("channel %d missing: %v", row.ChannelID, err))
		return
	}
	if !ch.Enabled {
		row.State, row.Reason = stateSuppressed, reasonDisabled
		if err := d.repo.updateDelivery(ctx, d.repo.st.DB(), row); err != nil {
			d.logger.Error("notify: update delivery", "id", row.ID, "err", err)
		}
		return
	}
	secret, err := d.repo.OpenSecret(sctx, ch.ID)
	if err != nil {
		d.fail(ctx, row, &ch, permanent("credentials unreadable: re-enter them on the channel"))
		return
	}

	p := payload{
		ID: ev.ID, Kind: ev.Kind, Severity: ev.Severity, Key: ev.Key, Subject: ev.Subject,
		Body: ev.Body, Source: ev.Source, At: ev.At, Resolved: ev.Resolved,
		Link: d.absLink(ev.Link), Fields: ev.Fields, Host: d.host,
	}
	err = d.sendTo(sctx, ch, secret, p)
	row.Attempts++
	if err == nil {
		row.State, row.NextAt, row.LastError = stateSent, nil, ""
		row.SentAt = &now
		if err := d.repo.updateDelivery(ctx, d.repo.st.DB(), row); err != nil {
			d.logger.Error("notify: update delivery", "id", row.ID, "err", err)
		}
		d.recovered(ctx, ch)
		return
	}
	d.fail(ctx, row, &ch, err)
}

// fail records an attempt that did not deliver: retried with backoff, or
// marked failed for good when the error is permanent or the attempts are spent.
func (d *Dispatcher) fail(ctx context.Context, row delivery, ch *Channel, err error) {
	now := d.now()
	row.LastError = clampText(err.Error(), 300)
	if row.Attempts == 0 {
		row.Attempts = 1
	}
	if isPermanent(err) || row.Attempts >= maxAttempts {
		row.State, row.NextAt = "failed", nil
		if err := d.repo.updateDelivery(ctx, d.repo.st.DB(), row); err != nil {
			d.logger.Error("notify: update delivery", "id", row.ID, "err", err)
		}
		if ch != nil {
			d.bellFailed(ctx, *ch, row.LastError)
		}
		return
	}
	wait := retryBackoff[min(row.Attempts-1, len(retryBackoff)-1)]
	if w, ok := retryWait(err); ok && w > wait {
		wait = w
	}
	next := now.Add(wait)
	row.NextAt = &next
	if err := d.repo.updateDelivery(ctx, d.repo.st.DB(), row); err != nil {
		d.logger.Error("notify: update delivery", "id", row.ID, "err", err)
	}
}

// bellFailed raises D10's event: bell-only, never routed to a channel, so a
// broken channel cannot keep sending about itself.
func (d *Dispatcher) bellFailed(ctx context.Context, ch Channel, reason string) {
	d.svc.emit(ctx, Event{
		Kind:     KindNotifyDeliveryFail,
		Severity: Warning,
		Key:      fmt.Sprintf("%s:%d", KindNotifyDeliveryFail, ch.ID),
		Subject:  fmt.Sprintf("Delivery to %s failed", ch.Name),
		Body:     reason,
		Source:   "notify",
	}, false)
}

// recovered closes D10's event for a channel that delivers again, if it is open.
func (d *Dispatcher) recovered(ctx context.Context, ch Channel) {
	key := fmt.Sprintf("%s:%d", KindNotifyDeliveryFail, ch.ID)
	st, found, err := d.repo.getState(ctx, d.repo.st.DB(), key)
	if err != nil || !found || !st.Open {
		return
	}
	d.svc.emit(ctx, Event{
		Kind:     KindNotifyDeliveryFail,
		Key:      key,
		Subject:  fmt.Sprintf("Delivery to %s works again", ch.Name),
		Source:   "notify",
		Resolved: true,
	}, false)
}

// sendTo dispatches on the channel type.
func (d *Dispatcher) sendTo(ctx context.Context, ch Channel, secret map[string]string, p payload) error {
	switch ch.Type {
	case TypeWebhook:
		return d.sendWebhook(ctx, ch, secret, p)
	case TypeDiscord:
		return d.sendDiscord(ctx, ch, secret, p)
	case TypeTelegram:
		return d.sendTelegram(ctx, ch, secret, p)
	case TypeSMTP:
		return d.sendSMTP(ctx, ch, secret, p)
	}
	return permanent("unknown channel type %q", ch.Type)
}

// absLink makes a panel-relative link absolute, or drops it when no panel URL is set.
func (d *Dispatcher) absLink(link string) string {
	if link == "" || d.panelURL == "" {
		return ""
	}
	return d.panelURL + link
}
