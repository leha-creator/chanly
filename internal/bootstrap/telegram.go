package bootstrap

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/integration/telegram"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

const telegramWebhookPattern = "POST " + BaseURL + "/webhooks/telegram"

func telegramClient(cfg Config, in *infra) *telegram.Client {
	if !cfg.Telegram.Enabled {
		return nil
	}
	return &telegram.Client{Token: cfg.Telegram.BotToken,
		Outbound: outbound.New(in.pool, in.clock, in.log, outbound.Options{System: "telegram", HTTP: telegramHTTPClient(cfg.Telegram.ProxyURL), Metrics: in.outMetrics, Alerter: in.alerter}),
		Limiter:  ratelimit.New(in.pool, in.clock, in.log, ratelimit.Options{Metrics: in.limitMetrics})}
}

// A dedicated transport confines proxy use to this integration.
func telegramHTTPClient(proxyURL string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxyURL != "" {
		proxy, _ := url.Parse(proxyURL) // Checked by Config.Validate.
		transport.Proxy = http.ProxyURL(proxy)
	}
	return outbound.NewHTTPClient(transport)
}

func newTelegramWebhook(cfg Config, in *infra, runner tx.Runner, queue job.Enqueuer) http.Handler {
	recorder := inbound.NewRecorder(in.pool, runner, queue, in.clock, in.log)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Telegram.Enabled {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if cfg.Telegram.WebhookSecret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token")), []byte(cfg.Telegram.WebhookSecret)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		id, start, ok, err := telegram.ParseStart(raw, cfg.Telegram.BotName, func(token string) string {
			return accountdomain.HashTelegramBotToken([]byte(cfg.AuthCodePepper), token)
		})
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if ok {
			payload, _ := json.Marshal(start)
			if _, err := recorder.Record(r.Context(), inbound.Event{Source: "telegram", ExternalKey: strconv.FormatInt(id, 10), EventType: "bot_started", Payload: payload}); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (m *modules) registerTelegram() {
	m.platform.dispatcher.Handle("telegram", "bot_started", func(ctx context.Context, e inbound.Event) error {
		var start telegram.Start
		if err := json.Unmarshal(e.Payload, &start); err != nil {
			return job.Permanent(err)
		}
		return m.account.ConfirmTelegramBotConnection(ctx, start.UserID, start.TokenHash)
	})
}
