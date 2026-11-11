package realtime

import (
	"errors"
	"sync"

	"github.com/leha-creator/chanly/internal/platform/httpx"
)

var ErrUnavailable = errors.New("realtime reader unavailable")

// Hub has bounded queues; a full queue disconnects only that subscriber.
// Subscribe and recovery share the same lock: ready cannot race past a failed reader.
type Hub struct {
	mu       sync.Mutex
	healthy  bool
	closed   bool
	capacity int
	clients  map[*Subscription]struct{}
}

type Subscription struct {
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
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.healthy || h.closed {
		return nil, ErrUnavailable
	}
	s := &Subscription{Events: make(chan Event, h.capacity), Done: make(chan struct{}), principal: p, admin: admin, hub: h}
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
