package httpx

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Middleware — промежуточный слой net/http.
type Middleware func(http.Handler) http.Handler

// Chain применяет слои так, что первый в списке выполняется первым.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// RequestID берёт X-Request-Id из запроса, если это UUID, иначе создаёт новый;
// возвращает его в ответе и кладёт в context (07 §5.1).
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-Id")
			if _, err := uuid.Parse(id); err != nil || len(id) != 36 {
				id = ids.New().String()
			}
			w.Header().Set("X-Request-Id", id)
			next.ServeHTTP(w, r.WithContext(logx.WithRequestID(r.Context(), id)))
		})
	}
}

// Recover превращает панику в ответ 500 internal.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler {
						panic(v)
					}
					log.ErrorContext(r.Context(), "panic recovered", logx.KeyOp, "HTTP.Recover",
						"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
					WriteError(w, r, log, apperr.Internal(fmt.Errorf("panic: %v", v)))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequestObserver получает итог каждого запроса — для метрик.
type RequestObserver interface {
	ObserveHTTP(route, method string, status int, duration time.Duration)
}

// AccessLog пишет начало и итог запроса и передаёт их в метрики. Тело запроса
// и cookie не пишутся. Маршрут — operationId, который определяет слой ниже (SetRoute).
func AccessLog(log *slog.Logger, clk clock.Clock, obs RequestObserver) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := clk.Now()
			holder := &routeHolder{}
			ctx := context.WithValue(r.Context(), routeKey{}, holder)
			service := isServicePath(r.URL.Path)
			if !service {
				log.DebugContext(ctx, "http request started", logx.KeyOp, "HTTP.Request", "method", r.Method, "path", RedactPath(r.URL.Path))
			}

			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r.WithContext(ctx))
			if sw.status == 0 {
				sw.status = http.StatusOK
			}
			dur := clk.Now().Sub(start)
			route := holder.get()
			if route == "" {
				route = "unmatched"
				if service {
					route = r.URL.Path
				}
			}
			if obs != nil {
				obs.ObserveHTTP(route, r.Method, sw.status, dur)
			}
			attrs := []any{logx.KeyOp, "HTTP.Request", "method", r.Method, "path", RedactPath(r.URL.Path), "route", route,
				"status", sw.status, "bytes", sw.bytes, "duration_ms", dur.Milliseconds()}
			switch {
			case sw.status >= 500:
				log.ErrorContext(ctx, "http request finished", attrs...)
			case service:
				log.DebugContext(ctx, "http request finished", attrs...)
			default:
				log.InfoContext(ctx, "http request finished", attrs...)
			}
		})
	}
}

func isServicePath(p string) bool {
	return p == "/livez" || p == "/readyz" || p == "/metrics"
}

type routeKey struct{}

type routeHolder struct {
	mu    sync.Mutex
	route string
}

func (h *routeHolder) get() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.route
}

// SetRoute сообщает AccessLog имя маршрута (operationId) для журнала и метрик.
func SetRoute(ctx context.Context, route string) {
	if h, ok := ctx.Value(routeKey{}).(*routeHolder); ok {
		h.mu.Lock()
		h.route = route
		h.mu.Unlock()
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// SecurityHeaders — заголовки безопасности для кабинетов (07 §4, «Транспорт»).
func SecurityHeaders(apiPrefix string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("X-Frame-Options", "DENY")
			if strings.HasPrefix(r.URL.Path, apiPrefix) {
				h.Set("Cache-Control", "no-store")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit ограничивает тело запроса. limitFor возвращает предел для запроса.
func BodyLimit(limitFor func(r *http.Request) int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limitFor(r))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// secretPathPrefixes — пути, у которых следующий сегмент секретный (адрес Webhook
// MAX, Q-35). В журнал и метрики сегмент не попадает.
var secretPathPrefixes = []string{"/webhooks/max/"}

// RedactPath заменяет секретный сегмент пути на "***".
func RedactPath(p string) string {
	for _, prefix := range secretPathPrefixes {
		i := strings.Index(p, prefix)
		if i < 0 {
			continue
		}
		rest := p[i+len(prefix):]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			return p[:i+len(prefix)] + "***" + rest[j:]
		}
		return p[:i+len(prefix)] + "***"
	}
	return p
}
