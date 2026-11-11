package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/store"
)

type streamResponse struct {
	ctx    context.Context
	stream *store.Stream
}

func (h *Handlers) SubscribeEvents(ctx context.Context, req api.SubscribeEventsRequestObject) (api.SubscribeEventsResponseObject, error) {
	s, err := h.st.Subscribe(tokenFrom(ctx), false)
	if err != nil {
		return nil, err
	}
	return streamResponse{ctx: ctx, stream: s}, nil
}

func (h *Handlers) SubscribeAdminEvents(ctx context.Context, req api.SubscribeAdminEventsRequestObject) (api.SubscribeAdminEventsResponseObject, error) {
	s, err := h.st.Subscribe(tokenFrom(ctx), true)
	if err != nil {
		return nil, err
	}
	return streamResponse{ctx: ctx, stream: s}, nil
}

func (s streamResponse) VisitSubscribeEventsResponse(w http.ResponseWriter) error { return s.serve(w) }
func (s streamResponse) VisitSubscribeAdminEventsResponse(w http.ResponseWriter) error {
	return s.serve(w)
}

func (s streamResponse) serve(w http.ResponseWriter) error {
	defer s.stream.Close()
	c := http.NewResponseController(w)
	frame := func(name string, body any) bool {
		if !s.stream.Valid() {
			return false
		}
		select {
		case <-s.stream.Done:
			return false
		case <-s.ctx.Done():
			return false
		default:
		}
		if c.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
			return false
		}
		b, err := json.Marshal(body)
		if err != nil || len(b) > 4096 {
			return false
		}
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b); err != nil {
			return false
		}
		if c.Flush() != nil {
			return false
		}
		return c.SetWriteDeadline(time.Time{}) == nil
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	if !frame("ready", map[string]int{"version": 1}) {
		return nil
	}
	heart := time.NewTicker(15 * time.Second)
	defer heart.Stop()
	access := time.NewTicker(time.Second)
	defer access.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return nil
		case <-s.stream.Done:
			return nil
		case <-access.C:
			if !s.stream.Valid() {
				return nil
			}
		case <-heart.C:
			if !frame("heartbeat", map[string]int{"version": 1}) {
				return nil
			}
		case e := <-s.stream.Events:
			if !frame(e.Name, e.Data) {
				return nil
			}
		}
	}
}
