package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"time"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
)

const (
	HeartbeatInterval   = 15 * time.Second
	AccessCheckInterval = time.Second
	AccessCheckTimeout  = 2 * time.Second
	WriteTimeout        = 5 * time.Second
)

// Handler must be registered INSIDE the Session/AdminGate chain.
// Authenticate must check without touching/extending the session expiration.
func Handler(hub *Hub, auth httpx.Authenticator, admin bool, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := "subscribeEvents"
		if admin {
			route = "subscribeAdminEvents"
		}
		httpx.SetRoute(r.Context(), route)
		p, ok := httpx.PrincipalFrom(r.Context())
		if !ok {
			httpx.WriteError(w, r, log, apperr.Unauthorized("session is missing"))
			return
		}
		if admin {
			if _, err := httpx.RequireAdmin(r.Context(), log, "Realtime.Subscribe", httpx.AdminAny...); err != nil {
				httpx.WriteError(w, r, log, err)
				return
			}
		}
		cookie, err := r.Cookie(httpx.SessionCookie)
		if err != nil {
			httpx.WriteError(w, r, log, apperr.Unauthorized("session is missing"))
			return
		}
		check := func() bool {
			ctx, cancel := context.WithTimeout(r.Context(), AccessCheckTimeout)
			defer cancel()
			current, err := auth(ctx, cookie.Value)
			return err == nil && reflect.DeepEqual(p, current)
		}
		if !check() {
			httpx.WriteError(w, r, log, apperr.Unauthorized("session access changed"))
			return
		}
		sub, err := hub.Subscribe(p, admin)
		if err != nil {
			w.Header().Set("Retry-After", "1")
			httpx.WriteError(w, r, log, apperr.New(apperr.CodeServiceUnavailable, "realtime reader unavailable"))
			return
		}
		defer sub.Close()
		reason := "write_or_cancel"
		log.InfoContext(r.Context(), "realtime stream subscribed", "admin", admin)
		defer func() { log.InfoContext(r.Context(), "realtime stream closed", "admin", admin, "reason", reason) }()
		controller := http.NewResponseController(w)
		// Refresh only this stream's write deadline for every frame; normal HTTP
		// requests retain the server's 60s WriteTimeout. No HTTP/1-only headers.
		frame := func(name string, body any) bool {
			select {
			case <-sub.Done:
				return false
			case <-r.Context().Done():
				return false
			default:
			}
			if err := controller.SetWriteDeadline(time.Now().Add(WriteTimeout)); err != nil {
				return false
			}
			data, err := json.Marshal(body)
			if err != nil || len(data) > 4096 {
				return false
			}
			if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
				return false
			}
			if err = controller.Flush(); err != nil {
				return false
			}
			// Clear the per-frame deadline while waiting for the next heartbeat.
			return controller.SetWriteDeadline(time.Time{}) == nil
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		if !frame("ready", map[string]int{"version": 1}) {
			return
		}
		heart := time.NewTicker(HeartbeatInterval)
		defer heart.Stop()
		access := time.NewTicker(AccessCheckInterval)
		defer access.Stop()
		for {
			select {
			case <-r.Context().Done():
				reason = "request_cancelled"
				return
			case <-sub.Done:
				reason = "reader_or_queue"
				return
			case <-access.C:
				if !check() {
					reason = "access_changed"
					return
				}
			case <-heart.C:
				if !check() || !frame("heartbeat", map[string]int{"version": 1}) {
					return
				}
			case event := <-sub.Events:
				log.DebugContext(r.Context(), "realtime invalidate", "topic", event.Topic)
				if !check() || !frame("invalidate", event) {
					return
				}
			}
		}
	})
}
