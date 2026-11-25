package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func adminFor(t *testing.T) (*Admin, *Repo) {
	t.Helper()
	svc, r, _ := engine(t, nil)
	d := NewDispatcher(DispatcherOptions{Service: svc, Host: "panel-test"})
	return NewAdmin(svc, d), r
}

func TestAdminCreateRefusesIncompleteChannel(t *testing.T) {
	a, _ := adminFor(t)
	ctx := context.Background()
	// A telegram channel without its bot token is refused before anything is stored.
	_, err := a.CreateChannel(ctx, ChannelWrite{Name: "tg", Type: TypeTelegram, Config: map[string]any{"chat_id": "1"}})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("CreateChannel without token = %v, want ErrInvalidEvent", err)
	}
	if chs, _ := a.ListChannels(ctx); len(chs) != 0 {
		t.Fatalf("refused channel was stored: %+v", chs)
	}
}

func TestAdminUpdateKeepsSecretAndValidatesMerged(t *testing.T) {
	a, _ := adminFor(t)
	ctx := context.Background()
	ch, err := a.CreateChannel(ctx, ChannelWrite{
		Name: "tg", Type: TypeTelegram, Config: map[string]any{"chat_id": "1"},
		Secret: map[string]string{"bot_token": "t0k"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The token is not resent: the stored one satisfies the rule.
	w := ChannelWrite{Name: "tg2", Type: TypeTelegram, Config: map[string]any{"chat_id": "2"}}
	if _, err := a.UpdateChannel(ctx, ch.ID, w); err != nil {
		t.Fatalf("update without a token: %v", err)
	}
	// Clearing the token leaves the channel unable to send, so the update is refused.
	w.Secret = map[string]string{"bot_token": ""}
	if _, err := a.UpdateChannel(ctx, ch.ID, w); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("clearing the required token = %v, want ErrInvalidEvent", err)
	}
}

func TestAdminTestSendIsLoggedAsDelivery(t *testing.T) {
	a, _ := adminFor(t)
	ctx := context.Background()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ch, err := a.CreateChannel(ctx, ChannelWrite{
		Name: "hook", Type: TypeWebhook, Config: map[string]any{"url": srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := a.TestChannel(ctx, ch.ID); err != nil {
		t.Fatalf("TestChannel: %v", err)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
	rows, err := a.ListDeliveries(ctx, DeliveryFilter{ChannelID: ch.ID})
	if err != nil || len(rows) != 1 || rows[0].State != stateSent || rows[0].Kind != KindNotifyTest {
		t.Fatalf("test send not logged: %+v, %v", rows, err)
	}
}

func TestAdminTestSendFailureReturnsError(t *testing.T) {
	a, _ := adminFor(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	ch, err := a.CreateChannel(ctx, ChannelWrite{
		Name: "denied", Type: TypeWebhook, Config: map[string]any{"url": srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.TestChannel(ctx, ch.ID); err == nil {
		t.Fatal("a 403 from the receiver was reported as success")
	}
}
