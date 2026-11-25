package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/portbyte-id/server-panel/internal/notify"
)

// NotifyService is what the notification routes call (§4.5). *notify.Admin
// implements it. A nil NotifyService leaves every /api/notify route answering 503.
type NotifyService interface {
	Kinds() []notify.KindInfo
	Summary(ctx context.Context) (notify.Summary, error)
	ListEvents(ctx context.Context, f notify.EventFilter) ([]notify.StoredEvent, error)
	MarkRead(ctx context.Context, ids []int64) (int64, error)
	MarkAllRead(ctx context.Context) (int64, error)
	Subscribe() (<-chan notify.StoredEvent, func())
	ListChannels(ctx context.Context) ([]notify.Channel, error)
	GetChannel(ctx context.Context, id int64) (notify.Channel, error)
	CreateChannel(ctx context.Context, w notify.ChannelWrite) (notify.Channel, error)
	UpdateChannel(ctx context.Context, id int64, w notify.ChannelWrite) (notify.Channel, error)
	DeleteChannel(ctx context.Context, id int64) error
	TestChannel(ctx context.Context, id int64) error
	ListDeliveries(ctx context.Context, f notify.DeliveryFilter) ([]notify.DeliveryRow, error)
	RetryDelivery(ctx context.Context, id int64) error
}

// notifyHeartbeat keeps the stream open through proxies; sse.go uses the same interval.
const notifyHeartbeat = 15 * time.Second

func (s *server) notifyRoutes(mux *http.ServeMux) {
	h := func(fn func(http.ResponseWriter, *http.Request, NotifyService)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Notify == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"notifications are not available; see the daemon log")
				return
			}
			fn(w, r, s.deps.Notify)
		}))
	}
	mux.Handle("GET /api/notify/events", h(notifyListEvents))
	mux.Handle("GET /api/notify/summary", h(notifySummary))
	mux.Handle("POST /api/notify/events/read", h(notifyMarkRead))
	mux.Handle("GET /api/notify/stream", h(notifyStream))
	mux.Handle("GET /api/notify/kinds", h(notifyKinds))
	mux.Handle("GET /api/notify/channels", h(notifyListChannels))
	mux.Handle("POST /api/notify/channels", h(notifyCreateChannel))
	mux.Handle("GET /api/notify/channels/{id}", h(notifyGetChannel))
	mux.Handle("PUT /api/notify/channels/{id}", h(notifyUpdateChannel))
	mux.Handle("DELETE /api/notify/channels/{id}", h(notifyDeleteChannel))
	mux.Handle("POST /api/notify/channels/{id}/test", h(notifyTestChannel))
	mux.Handle("GET /api/notify/deliveries", h(notifyListDeliveries))
	mux.Handle("POST /api/notify/deliveries/{id}/retry", h(notifyRetryDelivery))
}

// notifyChannelBody is the JSON of a channel write. Enabled and send_resolved
// default to true when omitted.
type notifyChannelBody struct {
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	Enabled      *bool             `json:"enabled"`
	MinSeverity  string            `json:"min_severity"`
	Kinds        []string          `json:"kinds"`
	QuietStart   string            `json:"quiet_start"`
	QuietEnd     string            `json:"quiet_end"`
	SendResolved *bool             `json:"send_resolved"`
	Config       map[string]any    `json:"config"`
	Secret       map[string]string `json:"secret"`
}

func (b notifyChannelBody) write() notify.ChannelWrite {
	return notify.ChannelWrite{
		Name:         b.Name,
		Type:         b.Type,
		Enabled:      b.Enabled == nil || *b.Enabled,
		MinSeverity:  notify.Severity(b.MinSeverity),
		Kinds:        b.Kinds,
		QuietStart:   b.QuietStart,
		QuietEnd:     b.QuietEnd,
		SendResolved: b.SendResolved == nil || *b.SendResolved,
		Config:       b.Config,
		Secret:       b.Secret,
	}
}

func notifyDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return false
	}
	return true
}

// notifyFail maps the notify package's errors to statuses.
func notifyFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, notify.ErrNotFound):
		writeError(w, http.StatusNotFound, errNotFound, "no such notification item")
	case errors.Is(err, notify.ErrNameTaken):
		writeError(w, http.StatusConflict, errConflict, "a channel with that name exists")
	case errors.Is(err, notify.ErrInvalidState):
		writeError(w, http.StatusConflict, errConflict, "the delivery is not failed, so it cannot be retried")
	case errors.Is(err, notify.ErrNoSecretKey):
		writeError(w, http.StatusServiceUnavailable, errUnavailable,
			"channel credentials need data/secret.key, which this host does not have")
	case errors.Is(err, notify.ErrInvalidEvent):
		writeError(w, http.StatusUnprocessableEntity, errInvalidInput, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, errInternal, "notification request failed; see the daemon log")
	}
}

func notifyID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusNotFound, errNotFound, "no such notification item")
		return 0, false
	}
	return id, true
}

func notifyQueryInt(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	return n
}

func notifyListEvents(w http.ResponseWriter, r *http.Request, n NotifyService) {
	events, err := n.ListEvents(r.Context(), notify.EventFilter{
		Limit:      int(notifyQueryInt(r, "limit")),
		BeforeID:   notifyQueryInt(r, "before"),
		UnreadOnly: r.URL.Query().Get("unread") == "1",
	})
	if err != nil {
		notifyFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func notifySummary(w http.ResponseWriter, r *http.Request, n NotifyService) {
	sum, err := n.Summary(r.Context())
	if err != nil {
		notifyFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func notifyMarkRead(w http.ResponseWriter, r *http.Request, n NotifyService) {
	var body struct {
		IDs []int64 `json:"ids"`
		All bool    `json:"all"`
	}
	if !notifyDecode(w, r, &body) {
		return
	}
	var (
		changed int64
		err     error
	)
	if body.All {
		changed, err = n.MarkAllRead(r.Context())
	} else {
		changed, err = n.MarkRead(r.Context(), body.IDs)
	}
	if err != nil {
		notifyFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"updated": changed})
}

// notifyStream sends each recorded event as "event: notification" until the
// client goes away. A comment line keeps idle connections open.
func notifyStream(w http.ResponseWriter, r *http.Request, n NotifyService) {
	st := startSSE(w)
	events, unsub := n.Subscribe()
	defer unsub()
	heartbeat := time.NewTicker(notifyHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok || !st.event("notification", ev) {
				return
			}
		case <-heartbeat.C:
			if !st.comment("keepalive") {
				return
			}
		}
	}
}

func notifyKinds(w http.ResponseWriter, _ *http.Request, n NotifyService) {
	writeJSON(w, http.StatusOK, n.Kinds())
}

func notifyListChannels(w http.ResponseWriter, r *http.Request, n NotifyService) {
	chs, err := n.ListChannels(r.Context())
	if err != nil {
		notifyFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, chs)
}

func notifyGetChannel(w http.ResponseWriter, r *http.Request, n NotifyService) {
	id, ok := notifyID(w, r)
	if !ok {
		return
	}
	ch, err := n.GetChannel(r.Context(), id)
	if err != nil {
		notifyFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func notifyCreateChannel(w http.ResponseWriter, r *http.Request, n NotifyService) {
	var body notifyChannelBody
	if !notifyDecode(w, r, &body) {
		return
	}
	ch, err := n.CreateChannel(r.Context(), body.write())
	if err != nil {
		notifyFail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ch)
}

func notifyUpdateChannel(w http.ResponseWriter, r *http.Request, n NotifyService) {
	id, ok := notifyID(w, r)
	if !ok {
		return
	}
	var body notifyChannelBody
	if !notifyDecode(w, r, &body) {
		return
	}
	ch, err := n.UpdateChannel(r.Context(), id, body.write())
	if err != nil {
		notifyFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func notifyDeleteChannel(w http.ResponseWriter, r *http.Request, n NotifyService) {
	id, ok := notifyID(w, r)
	if !ok {
		return
	}
	if err := n.DeleteChannel(r.Context(), id); err != nil {
		notifyFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// notifyTestChannel answers the synchronous test send as {ok, error}. A send
// failure is an answer, not an HTTP error.
func notifyTestChannel(w http.ResponseWriter, r *http.Request, n NotifyService) {
	id, ok := notifyID(w, r)
	if !ok {
		return
	}
	err := n.TestChannel(r.Context(), id)
	if err != nil && (errors.Is(err, notify.ErrNotFound) || errors.Is(err, notify.ErrNoSecretKey)) {
		notifyFail(w, err)
		return
	}
	res := map[string]any{"ok": err == nil}
	if err != nil {
		res["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, res)
}

func notifyListDeliveries(w http.ResponseWriter, r *http.Request, n NotifyService) {
	rows, err := n.ListDeliveries(r.Context(), notify.DeliveryFilter{
		ChannelID: notifyQueryInt(r, "channel"),
		State:     r.URL.Query().Get("state"),
		Limit:     int(notifyQueryInt(r, "limit")),
		BeforeID:  notifyQueryInt(r, "before"),
	})
	if err != nil {
		notifyFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func notifyRetryDelivery(w http.ResponseWriter, r *http.Request, n NotifyService) {
	id, ok := notifyID(w, r)
	if !ok {
		return
	}
	if err := n.RetryDelivery(r.Context(), id); err != nil {
		notifyFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"queued": true})
}
