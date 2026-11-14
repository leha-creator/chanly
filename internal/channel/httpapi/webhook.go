// Package httpapi — HTTP модуля каналов: приём Webhook MAX и операции кабинета блогера.
package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// WebhookBodyLimit — предел тела Webhook (07 §4).
const WebhookBodyLimit = 1 << 20

// Порог оповещения об отказах проверки подлинности (07 §5.3: «при повторении»).
const (
	authFailureThreshold = 5
	authFailureWindow    = 10 * time.Minute
)

// Recorder сохраняет входящее событие и ставит его разбор (platform/inbound).
type Recorder interface {
	Record(ctx context.Context, e inbound.Event) (duplicate bool, err error)
}

// Alerter — оповещения администратору.
type Alerter interface {
	Raise(ctx context.Context, a alert.Alert) error
}

// WebhookOptions — секреты приёма (R-3).
type WebhookOptions struct {
	ConnectionPepper []byte
	// PathSecret — секретная часть адреса /webhooks/max/{secret}.
	PathSecret string
	// HeaderSecret — секрет подписки, который MAX передаёт в заголовке SecretHeader.
	// Пусто — заголовок не проверяется (только в разработке, проверяет конфигурация).
	HeaderSecret string
	// SecretHeader — имя заголовка (max.SecretHeader).
	SecretHeader string
}

// Webhook — приём событий MAX (05 §2.2, шаги 1–4). Разбор выполняет задание
// process_inbound_event: в запросе нет ни вызовов MAX, ни сценариев (07 §7).
type Webhook struct {
	rec    Recorder
	alerts Alerter
	clock  clock.Clock
	log    *slog.Logger
	opts   WebhookOptions
	total  *prometheus.CounterVec

	mu       sync.Mutex
	failures []time.Time
}

// Registerer — реестр метрик (platform/metrics.Registry или prometheus.Registry).
type Registerer interface{ MustRegister(...prometheus.Collector) }

// NewWebhook создаёт приём. reg может быть nil (метрики не пишутся).
func NewWebhook(rec Recorder, alerts Alerter, clk clock.Clock, log *slog.Logger, reg Registerer, opts WebhookOptions) *Webhook {
	w := &Webhook{rec: rec, alerts: alerts, clock: clk, log: log.With(logx.KeyModule, "channel"), opts: opts}
	w.total = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "maxim_max_webhook_requests_total", Help: "MAX webhook requests by outcome.",
	}, []string{"outcome"})
	if reg != nil {
		reg.MustRegister(w.total)
	}
	return w
}

// Исходы приёма для метрик.
const (
	outcomeAccepted     = "accepted"
	outcomeDuplicate    = "duplicate"
	outcomeUnauthorized = "unauthorized"
	outcomeInvalid      = "invalid"
	outcomeError        = "error"
)

// ServeHTTP обрабатывает POST /api/v1/webhooks/max/{secret}.
func (h *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const op = "Channel.MAXWebhook"
	ctx := r.Context()
	httpx.SetRoute(ctx, "maxWebhook")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if reason := h.authenticate(r); reason != "" {
		h.total.WithLabelValues(outcomeUnauthorized).Inc()
		h.log.WarnContext(ctx, "inbound event rejected", logx.KeyOp, op, "source", inbound.SourceMAX, "reason", reason,
			"remote_addr", httpx.ClientIP(ctx))
		h.authFailed(ctx, reason)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, WebhookBodyLimit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.total.WithLabelValues(outcomeInvalid).Inc()
			h.log.WarnContext(ctx, "inbound event too large", logx.KeyOp, op, "source", inbound.SourceMAX, "limit", WebhookBodyLimit)
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		h.total.WithLabelValues(outcomeInvalid).Inc()
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var head struct {
		UpdateType string `json:"update_type"`
	}
	if err := json.Unmarshal(body, &head); err != nil || head.UpdateType == "" {
		// Ответ 200: повтор того же тела ничего не исправит, а MAX будет повторять доставку.
		h.total.WithLabelValues(outcomeInvalid).Inc()
		h.log.WarnContext(ctx, "inbound event malformed", logx.KeyOp, op, "source", inbound.SourceMAX, "bytes", len(body))
		w.WriteHeader(http.StatusOK)
		return
	}
	externalKey := inbound.KeyFromBody(body)
	if head.UpdateType == "bot_started" {
		// A caller-supplied internal hash is never trusted. Replace the token
		// before storing the webhook; the worker only needs its HMAC.
		var event map[string]json.RawMessage
		if err := json.Unmarshal(body, &event); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload string
		payloadFields := 0
		for key, value := range event {
			switch {
			case strings.EqualFold(key, "connection_token_hash"), strings.EqualFold(key, "notification_token_hash"):
				delete(event, key)
			case strings.EqualFold(key, "payload"):
				payloadFields++
				_ = json.Unmarshal(value, &payload)
				delete(event, key)
			}
		}
		// encoding/json accepts case variants of field names. Ambiguous payloads
		// must neither survive redaction nor choose a token by random map order.
		if payloadFields != 1 {
			payload = ""
		}
		if len(h.opts.ConnectionPepper) > 0 && strings.HasPrefix(payload, "connect_") && len(payload) == 51 {
			hash := domain.HashConnectionToken(h.opts.ConnectionPepper, strings.TrimPrefix(payload, "connect_"))
			event["connection_token_hash"], _ = json.Marshal(hash)
		}
		if len(h.opts.ConnectionPepper) > 0 && strings.HasPrefix(payload, "notify_") {
			token := strings.TrimPrefix(payload, "notify_")
			raw, err := base64.RawURLEncoding.DecodeString(token)
			if err == nil && len(token) == 43 && len(raw) == 32 {
				mac := hmac.New(sha256.New, h.opts.ConnectionPepper)
				_, _ = mac.Write([]byte("notification-max-bot:" + token))
				event["notification_token_hash"], _ = json.Marshal(hex.EncodeToString(mac.Sum(nil)))
			}
		}
		body, _ = json.Marshal(event)
	}
	dup, err := h.rec.Record(ctx, inbound.Event{
		Source: inbound.SourceMAX, ExternalKey: externalKey, EventType: head.UpdateType, Payload: body,
	})
	if err != nil {
		h.total.WithLabelValues(outcomeError).Inc()
		h.log.ErrorContext(ctx, "inbound event not recorded", logx.KeyOp, op, "event_type", head.UpdateType, logx.KeyError, err.Error())
		w.WriteHeader(http.StatusInternalServerError) // MAX повторит доставку
		return
	}
	if dup {
		h.total.WithLabelValues(outcomeDuplicate).Inc()
	} else {
		h.total.WithLabelValues(outcomeAccepted).Inc()
	}
	w.WriteHeader(http.StatusOK)
}

// authenticate возвращает причину отказа или пустую строку (R-3).
func (h *Webhook) authenticate(r *http.Request) string {
	if h.opts.PathSecret == "" || !equal(r.PathValue("secret"), h.opts.PathSecret) {
		return "path"
	}
	if h.opts.HeaderSecret != "" && !equal(r.Header.Get(h.opts.SecretHeader), h.opts.HeaderSecret) {
		return "header"
	}
	return ""
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// authFailed поднимает оповещение, если отказы повторяются (07 §5.3). Счётчик —
// в памяти экземпляра: оповещение о подборе адреса не требует точности.
func (h *Webhook) authFailed(ctx context.Context, reason string) {
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
		Reason: "webhook_auth_failed:max", Level: alert.LevelWarn,
		Summary: "Повторные запросы на адрес Webhook MAX не прошли проверку подлинности",
		Fields:  map[string]any{"failures": n, "window": authFailureWindow.String(), "last_reason": reason},
	}); err != nil {
		h.log.ErrorContext(ctx, "webhook auth alert failed", logx.KeyOp, "Channel.MAXWebhook", logx.KeyError, err.Error())
	}
}
