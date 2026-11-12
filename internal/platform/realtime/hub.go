package realtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/leha-creator/chanly/internal/platform/httpx"
)

var ErrUnavailable = errors.New("realtime reader unavailable")

// Hub has bounded queues; a full queue disconnects only that subscriber.
// Subscribe and recovery share the same lock: ready cannot race past a failed reader.
type Hub struct {
	mu               sync.Mutex
	healthy          bool
	closed           bool
	capacity         int
	clients          map[*Subscription]struct{}
	creationBoundary func(context.Context) (time.Time, error)
}

type Subscription struct {
	startedAt time.Time
	Events    chan Event
	Done      chan struct{}
	principal *httpx.Principal
	admin     bool
	hub       *Hub
}

func NewHub(capacity int) *Hub {
	if capacity < 1 {
		capacity = 128
	}
	return &Hub{capacity: capacity, clients: make(map[*Subscription]struct{})}
}

func (h *Hub) Subscribe(p *httpx.Principal, admin bool) (*Subscription, error) {
	return h.subscribeAt(p, admin, time.Now())
}

// SubscribeContext gets the creation cutoff from the same PostgreSQL clock as
// the journal. API/DB clock skew must never turn old records into live signals.
func (h *Hub) SubscribeContext(ctx context.Context, p *httpx.Principal, admin bool) (*Subscription, error) {
	if h.creationBoundary == nil {
		return h.Subscribe(p, admin)
	}
	boundary, err := h.creationBoundary(ctx)
	if err != nil {
		return nil, ErrUnavailable
	}
	return h.subscribeAt(p, admin, boundary)
}
func (h *Hub) subscribeAt(p *httpx.Principal, admin bool, boundary time.Time) (*Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.healthy || h.closed {
		return nil, ErrUnavailable
	}
	s := &Subscription{startedAt: boundary, Events: make(chan Event, h.capacity), Done: make(chan struct{}), principal: p, admin: admin, hub: h}
	h.clients[s] = struct{}{}
	return s, nil
}

func (s *Subscription) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.remove(s)
}

func (h *Hub) remove(s *Subscription) {
	if _, ok := h.clients[s]; ok {
		delete(h.clients, s)
		close(s.Done)
	}
}

func (h *Hub) Publish(r Record) {
	if r.Scope == "catalog" {
		r.Event.ObjectID = nil
		r.Event.OrderID = nil
		r.Event.CampaignID = nil
		r.Event.ChannelID = nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.healthy || h.closed {
		return
	}
	for s := range h.clients {
		if r.Event.Topic == "notification.created" {
			if !r.CreatedAt.After(s.startedAt) {
				continue
			}
			r.Event.Mode = "live"
		}
		if !r.allowed(s.principal, s.admin) {
			continue
		}
		select {
		case s.Events <- r.Event:
		default:
			h.remove(s)
		}
	}
}

func (h *Hub) setHealthy(healthy bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.healthy = healthy && !h.closed
	if !healthy {
		for s := range h.clients {
			h.remove(s)
		}
	}
}

func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.healthy = false
	for s := range h.clients {
		h.remove(s)
	}
}
