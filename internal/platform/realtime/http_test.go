package realtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// A failed/slow writer must have a deadline and release its registered queue.
type failedStreamWriter struct {
	header   http.Header
	deadline time.Time
}

func (w *failedStreamWriter) Header() http.Header       { return w.header }
func (w *failedStreamWriter) WriteHeader(int)           {}
func (w *failedStreamWriter) Write([]byte) (int, error) { return 0, errors.New("client write stalled") }
func (w *failedStreamWriter) Flush()                    {}
func (w *failedStreamWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestFailedWriterDeadlineAndSubscriptionCleanup(t *testing.T) {
	h := NewHub(1)
	h.setHealthy(true)
	p := &httpx.Principal{UserID: uuid.New()}
	r := httptest.NewRequest("GET", "/events", nil)
	r = r.WithContext(httpx.WithPrincipal(r.Context(), p))
	r.AddCookie(&http.Cookie{Name: "session", Value: "fixture"})
	w := &failedStreamWriter{header: make(http.Header)}
	Handler(h, func(context.Context, string) (*httpx.Principal, error) { return p, nil }, false, logx.Discard()).ServeHTTP(w, r)
	if w.deadline.IsZero() || time.Until(w.deadline) > WriteTimeout {
		t.Fatal("unbounded stream write")
	}
	if len(h.clients) != 0 {
		t.Fatal("failed writer leaked subscription")
	}
}

func TestCatalogBroadcastAlwaysStripsIdentifiers(t *testing.T) {
	h := NewHub(2)
	h.setHealthy(true)
	s, _ := h.Subscribe(&httpx.Principal{Cabinets: []string{"advertiser"}}, false)
	id := uuid.New()
	h.Publish(Record{Scope: "catalog", Event: Event{Version: 1, Topic: "channel.changed", ObjectID: &id, ChannelID: &id, OrderID: &id, CampaignID: &id}})
	e := <-s.Events
	if e.ObjectID != nil || e.ChannelID != nil || e.OrderID != nil || e.CampaignID != nil {
		t.Fatal("catalog identifiers leaked")
	}
}
