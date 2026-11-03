// Package httpapi — HTTP модуля выплат: вебхук Рокет Ворк и операции кабинета блогера.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/payout/app"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// WebhookBodyLimit — предел тела вебхука (07 §4).
const WebhookBodyLimit = 1 << 20

// Порог оповещения об отказах проверки подписи (07 §5.3: «при повторении»).
const (
	authFailureThreshold = 5
	authFailureWindow    = 10 * time.Minute
	// AlertWebhookAuth — причина оповещения о повторных отказах подписи.
	AlertWebhookAuth = "webhook_auth_failed:rocketwork"
)

// Parser — проверка подписи и разбор события (app.Service.ParseWebhook).
type Parser interface {
	ParseWebhook(header map[string][]string, body []byte) (domain.WebhookEvent, error)
}

// Recorder сохраняет входящее событие и ставит его разбор (platform/inbound).
type Recorder interface {
	Record(ctx context.Context, e inbound.Event) (duplicate bool, err error)
}

// Alerter — оповещения администратору.
type Alerter interface {
	Raise(ctx context.Context, a alert.Alert) error
}

// Registerer — реестр метрик.
type Registerer interface{ MustRegister(...prometheus.Collector) }

// Webhook — приём событий Рокет Ворк (R-10, R-11). В запросе — только подпись, разбор
// и запись inbound_event; свежее состояние объекта запросит задание (I-13).
type Webhook struct {
	parser Parser
	rec    Recorder
	alerts Alerter
	clock  clock.Clock
	log    *slog.Logger
	total  *prometheus.CounterVec

	mu       sync.Mutex
	failures []time.Time
}

// NewWebhook создаёт приём. reg может быть nil.
func NewWebhook(parser Parser, rec Recorder, alerts Alerter, clk clock.Clock, log *slog.Logger, reg Registerer) *Webhook {
	w := &Webhook{parser: parser, rec: rec, alerts: alerts, clock: clk, log: log.With(logx.KeyModule, "payout")}
	w.total = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "maxim_rocketwork_webhook_requests_total", Help: "Rocket Work webhook requests by outcome.",
	}, []string{"outcome"})
	if reg != nil {
		reg.MustRegister(w.total)
	}
	return w
}

const (
	outcomeAccepted     = "accepted"
	outcomeDuplicate    = "duplicate"
	outcomeUnauthorized = "unauthorized"
	outcomeInvalid      = "invalid"
	outcomeDisabled     = "disabled"
	outcomeError        = "error"
)

// ServeHTTP обрабатывает POST /api/v1/webhooks/rocketwork.
func (h *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const op = "Payout.RocketWorkWebhook"
	ctx := r.Context()
	httpx.SetRoute(ctx, "rocketworkWebhook")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, WebhookBodyLimit))
	if err != nil {
		h.total.WithLabelValues(outcomeInvalid).Inc()
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.log.WarnContext(ctx, "rocketwork webhook rejected", logx.KeyOp, op, "reason", "too_large",
				"remote_ip", httpx.ClientIP(ctx), "limit", WebhookBodyLimit)
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ev, err := h.parser.ParseWebhook(r.Header, body)
	switch {
	case errors.Is(err, domain.ErrProviderDisabled):
		h.total.WithLabelValues(outcomeDisabled).Inc()
		w.WriteHeader(http.StatusNotFound)
		return
	case errors.Is(err, domain.ErrWebhookAuth):
		h.total.WithLabelValues(outcomeUnauthorized).Inc()
		h.log.WarnContext(ctx, "rocketwork webhook rejected", logx.KeyOp, op, "reason", "signature",
			"remote_ip", httpx.ClientIP(ctx))
		h.authFailed(ctx)
		w.WriteHeader(http.StatusUnauthorized)
		return
	case err != nil:
		// Ответ 200: повтор того же тела ничего не исправит (R-11).
		h.total.WithLabelValues(outcomeInvalid).Inc()
		h.log.WarnContext(ctx, "rocketwork webhook rejected", logx.KeyOp, op, "reason", "format",
			"remote_ip", httpx.ClientIP(ctx), "bytes", len(body), logx.KeyError, err.Error())
		w.WriteHeader(http.StatusOK)
		return
	}
	payload, _ := json.Marshal(app.EventPayload{ExternalID: ev.ExternalID, EventID: ev.EventID})
	key := ev.EventID
	if key == "" {
		key = inbound.KeyFromBody(body)
	}
	dup, err := h.rec.Record(ctx, inbound.Event{Source: inbound.SourceRocketWork, ExternalKey: key,
		EventType: string(ev.Kind), Payload: payload})
	if err != nil {
		h.total.WithLabelValues(outcomeError).Inc()
		h.log.ErrorContext(ctx, "rocketwork webhook not recorded", logx.KeyOp, op, "event_type", ev.Kind, logx.KeyError, err.Error())
		w.WriteHeader(http.StatusInternalServerError) // сервис повторит доставку
		return
	}
	if dup {
		h.total.WithLabelValues(outcomeDuplicate).Inc()
	} else {
		h.total.WithLabelValues(outcomeAccepted).Inc()
	}
	h.log.DebugContext(ctx, "rocketwork webhook received", logx.KeyOp, op, "event_type", ev.Kind, "duplicate", dup)
	w.WriteHeader(http.StatusOK)
}

// authFailed поднимает оповещение, если отказы повторяются (07 §5.3). Счётчик — в памяти
// экземпляра, как у вебхука MAX.
func (h *Webhook) authFailed(ctx context.Context) {
	now := h.clock.Now()
	h.mu.Lock()
	kept := h.failures[:0]
	for _, t := range h.failures {
		if now.Sub(t) < authFailureWindow {
			kept = append(kept, t)
		}
	}
	h.failures = append(kept, now)
	n := len(h.failures)
	h.mu.Unlock()
	if n < authFailureThreshold || h.alerts == nil {
		return
	}
	if err := h.alerts.Raise(ctx, alert.Alert{
		Reason: AlertWebhookAuth, Level: alert.LevelWarn,
		Summary: "Повторные запросы на вебхук Рокет Ворк не прошли проверку подписи",
		Fields:  map[string]any{"failures": n, "window": authFailureWindow.String()},
	}); err != nil {
		h.log.ErrorContext(ctx, "webhook auth alert failed", logx.KeyOp, "Payout.RocketWorkWebhook", logx.KeyError, err.Error())
	}
}
