package notify

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// testSendTimeout caps the synchronous test send of §4.5.
const testSendTimeout = 20 * time.Second

// Admin is what the HTTP API calls. It applies the channel rules (per-type
// validation, a merged secret on update) and runs the test send.
type Admin struct {
	svc  *Service
	repo *Repo
	disp *Dispatcher
}

// NewAdmin wires the API layer over the engine, the repository and the dispatcher.
func NewAdmin(svc *Service, disp *Dispatcher) *Admin {
	return &Admin{svc: svc, repo: svc.repo, disp: disp}
}

// Kinds lists the known kinds for the filter UI.
func (a *Admin) Kinds() []KindInfo { return Kinds() }

// Summary is the bell badge.
func (a *Admin) Summary(ctx context.Context) (Summary, error) { return a.repo.Summary(ctx) }

// ListEvents pages the event list.
func (a *Admin) ListEvents(ctx context.Context, f EventFilter) ([]StoredEvent, error) {
	return a.repo.ListEvents(ctx, f)
}

// MarkRead marks the given events read.
func (a *Admin) MarkRead(ctx context.Context, ids []int64) (int64, error) {
	return a.repo.MarkRead(ctx, ids)
}

// MarkAllRead marks every event read.
func (a *Admin) MarkAllRead(ctx context.Context) (int64, error) {
	return a.repo.MarkAllRead(ctx)
}

// Subscribe streams events as they are recorded, for the SSE endpoint.
func (a *Admin) Subscribe() (<-chan StoredEvent, func()) { return a.svc.Subscribe() }

// ListChannels returns the channels without their secrets.
func (a *Admin) ListChannels(ctx context.Context) ([]Channel, error) {
	return a.repo.ListChannels(ctx)
}

// GetChannel returns one channel without its secrets.
func (a *Admin) GetChannel(ctx context.Context, id int64) (Channel, error) {
	return a.repo.GetChannel(ctx, id)
}

// CreateChannel validates and stores a channel. The secret fields must be
// complete for the type; ErrNoSecretKey means this host cannot store one.
func (a *Admin) CreateChannel(ctx context.Context, w ChannelWrite) (Channel, error) {
	if a.repo.box == nil {
		return Channel{}, ErrNoSecretKey
	}
	if err := ValidateChannel(w, func(field string) string { return w.Secret[field] }); err != nil {
		return Channel{}, err
	}
	return a.repo.CreateChannel(ctx, w)
}

// UpdateChannel validates the channel as it will be after the write: a secret
// field not named keeps its stored value, and a field given "" is removed.
func (a *Admin) UpdateChannel(ctx context.Context, id int64, w ChannelWrite) (Channel, error) {
	if _, err := a.repo.GetChannel(ctx, id); err != nil {
		return Channel{}, err
	}
	stored, err := a.repo.OpenSecret(ctx, id)
	if err != nil {
		stored = map[string]string{}
	}
	merged := map[string]string{}
	for k, v := range stored {
		merged[k] = v
	}
	for k, v := range w.Secret {
		if v == "" {
			delete(merged, k)
		} else {
			merged[k] = v
		}
	}
	if err := ValidateChannel(w, func(field string) string { return merged[field] }); err != nil {
		return Channel{}, err
	}
	return a.repo.UpdateChannel(ctx, id, w)
}

// DeleteChannel removes a channel and its delivery rows.
func (a *Admin) DeleteChannel(ctx context.Context, id int64) error {
	return a.repo.DeleteChannel(ctx, id)
}

// ListDeliveries returns the delivery log.
func (a *Admin) ListDeliveries(ctx context.Context, f DeliveryFilter) ([]DeliveryRow, error) {
	return a.repo.ListDeliveries(ctx, f)
}

// RetryDelivery queues a failed delivery again and wakes the dispatcher.
func (a *Admin) RetryDelivery(ctx context.Context, id int64) error {
	if err := a.repo.RetryDelivery(ctx, id); err != nil {
		return err
	}
	a.svc.wake()
	return nil
}

// TestChannel sends one fixed test message now, bypassing filters, quiet
// hours, dedupe and rate (§4.5). The result is logged as a delivery.
func (a *Admin) TestChannel(ctx context.Context, id int64) error {
	return a.disp.testSend(ctx, id)
}

// testSend is the synchronous send behind TestChannel.
func (d *Dispatcher) testSend(ctx context.Context, id int64) error {
	ch, err := d.repo.GetChannel(ctx, id)
	if err != nil {
		return err
	}
	secret, err := d.repo.OpenSecret(ctx, id)
	if err != nil {
		return fmt.Errorf("credentials unreadable: re-enter them on the channel")
	}
	now := d.now()
	p := payload{
		Kind:     KindNotifyTest,
		Severity: Info,
		Key:      fmt.Sprintf("%s:%d", KindNotifyTest, id),
		Subject:  "Test notification from " + d.host,
		Body:     "This message checks that the channel delivers.",
		Source:   "notify",
		At:       now,
		Host:     d.host,
	}

	sctx, cancel := context.WithTimeout(ctx, testSendTimeout)
	defer cancel()
	sendErr := d.sendTo(sctx, ch, secret, p)

	// Log it as a delivery: the event goes to the list, the row says how it went.
	evID, err := d.repo.insertEvent(ctx, d.repo.st.DB(), Event{
		Kind: KindNotifyTest, Severity: Info, Key: p.Key, Subject: p.Subject,
		Body: p.Body, Source: "notify", At: now,
	})
	if err != nil {
		return err
	}
	row := delivery{EventID: evID, ChannelID: id, State: stateSent, Attempts: 1, CreatedAt: now, SentAt: &now}
	if sendErr != nil {
		row.State = "failed"
		row.LastError = redact(sendErr.Error(), secretValues(secret)...)
		row.SentAt = nil
	}
	if _, err := d.repo.insertDelivery(ctx, d.repo.st.DB(), row); err != nil {
		return err
	}
	if sendErr != nil {
		return errors.New(row.LastError)
	}
	return nil
}
