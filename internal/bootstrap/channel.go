package bootstrap

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	channelapp "github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/channel/domain"
	channelhttp "github.com/leha-creator/chanly/internal/channel/httpapi"
	channelpg "github.com/leha-creator/chanly/internal/channel/postgres"
	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// channelHandlers — псевдоним для встраивания в apiImpl рядом с обработчиками account.
type channelHandlers = channelhttp.Handlers

// maxWebhookPattern — маршрут приёма событий MAX (операция maxWebhook, R-4).
const maxWebhookPattern = "POST " + BaseURL + "/webhooks/max/{secret}"

// channelMAX — переходник адаптера MAX к портам модуля каналов (ARCHITECTURE.md,
// «Модуль → внешняя система»).
type channelMAX struct{ api maxAPI }

var (
	_ domain.ChatReader    = channelMAX{}
	_ domain.ChatManager   = channelMAX{}
	_ domain.MessageReader = channelMAX{}
)

func (c channelMAX) GetChat(ctx context.Context, chatID int64) (domain.ChatInfo, error) {
	chat, err := c.api.GetChat(ctx, chatID)
	if err != nil {
		return domain.ChatInfo{}, err
	}
	return domain.ChatInfo{
		Title: chat.Title, Description: chat.Description, IconURL: chat.IconURL(), Link: chat.Link,
		IsPublic: chat.IsPublic, IsChannel: chat.Type == max.ChatTypeChannel, SubscribersCount: chat.ParticipantsCount,
	}, nil
}

func (c channelMAX) GetMembership(ctx context.Context, chatID int64) (domain.Membership, error) {
	m, err := c.api.GetMembership(ctx, chatID)
	if err != nil {
		return domain.Membership{}, err
	}
	return domain.Membership{IsAdmin: m.IsAdmin, Permissions: m.Permissions}, nil
}

// ListMessages — сообщения канала для метрик (R-14); текст адаптер не читает.
func (c channelMAX) ListMessages(ctx context.Context, chatID int64, from, to time.Time, count int) ([]domain.MessageStat, error) {
	list, err := c.api.ListMessages(ctx, chatID, from, to, count)
	if err != nil {
		return nil, err
	}
	out := make([]domain.MessageStat, 0, len(list))
	for _, m := range list {
		out = append(out, domain.MessageStat{ID: m.MID, At: m.At, Views: m.Views, FromBot: m.FromBot})
	}
	return out, nil
}

func (c channelMAX) DeleteMessage(ctx context.Context, chatID int64, messageID string) error {
	return c.api.DeleteMessage(ctx, chatID, messageID)
}

func (c channelMAX) LeaveChat(ctx context.Context, chatID int64) error {
	return c.api.LeaveChat(ctx, chatID)
}

func (c channelMAX) SendUserMessage(ctx context.Context, userID int64, text string) (string, error) {
	return c.api.SendUserMessage(ctx, userID, text)
}

// newChannelService собирает модуль каналов. Порты MAX подключаются только в worker.
func newChannelService(cfg Config, in *infra, txRunner tx.Runner, queue job.Enqueuer, auditLog audit.Recorder) (*channelapp.Service, *channelpg.Store) {
	in.log.Debug("channel visibility policy configured", "op", "Bootstrap.Channel", "allow_private", cfg.channelVisibilityPolicy() == domain.AllowPrivateChannels)
	store := channelpg.New(in.pool, in.log.With(logx.KeyModule, "channel"))
	ch := cfg.Channel
	day := 24 * time.Hour
	deps := channelapp.Deps{
		Channels: store.Channels(), LinkCodes: store.LinkCodes(), Categories: store.Categories(), Counter: store.Categories(),
		Formats: store.Formats(), Prices: store.Prices(), Unavail: store.Unavailabilities(), Metrics: store.Metrics(),
		Catalog: store.Catalog(), Admin: store.AdminChannels(),
		Alerts: in.alerter, Tx: txRunner, Jobs: queue, Audit: auditLog, Clock: in.clock, Log: in.log,
		Config: channelapp.Config{
			Visibility:  cfg.channelVisibilityPolicy(),
			LinkCodeTTL: cfg.Channel.LinkCodeTTL, LinkCodesPerHour: cfg.Channel.LinkCodesPerHour,
			UnclaimedTTL: cfg.Channel.UnclaimedTTL, RknThreshold: cfg.Channel.RknThreshold,
			Pepper: []byte(cfg.AuthCodePepper), BotName: cfg.MAX.BotName,
			FeeRateBP: ch.FeeRateBP, RknRecheckInterval: ch.RknRecheckInterval,
			Unavailability: domain.UnavailabilityLimits{
				MaxLength: time.Duration(ch.UnavailabilityMaxDays) * day, Horizon: time.Duration(ch.UnavailabilityHorizonDays) * day,
				MaxCount: ch.UnavailabilityMaxCount,
			},
			Metrics: domain.MetricsConfig{SubscribersSpikeBP: ch.MetricsSubscribersSpikeBP, ViewsMinBP: ch.MetricsViewsMinBP},
		},
	}
	if in.max != nil {
		port := channelMAX{api: in.max}
		deps.Chats, deps.Bot, deps.Messages = port, port, port
	}
	// Availability и Placements не заданы: до модуля размещений работают пустые
	// реализации (R-11 этапа «Подключение каналов», R-8 этапа «Каталог»).
	return channelapp.New(deps), store
}

func (cfg Config) channelVisibilityPolicy() domain.VisibilityPolicy {
	if cfg.Env == EnvDevelopment {
		return domain.AllowPrivateChannels
	}
	return domain.PublicChannelsOnly
}

// newMAXWebhook — приём событий MAX (R-3, R-4) с данными клиента для журнала.
func newMAXWebhook(cfg Config, in *infra, txRunner tx.Runner, queue job.Enqueuer, trusted httpx.Middleware) http.Handler {
	log := in.log.With(logx.KeyModule, "inbound")
	rec := inbound.NewRecorder(in.pool, txRunner, queue, in.clock, log)
	var reg channelhttp.Registerer
	if in.reg != nil {
		reg = in.reg
	}
	h := channelhttp.NewWebhook(rec, in.alerter, in.clock, in.log, reg, channelhttp.WebhookOptions{
		PathSecret: cfg.MAX.WebhookPathSecret, HeaderSecret: cfg.MAX.WebhookSecret, SecretHeader: max.SecretHeader,
	})
	if cfg.MAX.WebhookPathSecret == "" {
		in.log.Warn("max webhook path secret is not set, all webhook requests are rejected", logx.KeyOp, "Bootstrap.Webhook")
	}
	return httpx.Chain(h, trusted)
}

// maxWebhookURL — полный адрес подписки: публичный адрес и секретная часть пути.
func maxWebhookURL(cfg Config) string {
	return strings.TrimRight(cfg.MAX.WebhookURL, "/") + "/" + cfg.MAX.WebhookPathSecret
}

// ensureMAXSubscription подписывает бота на Webhook, если подписки на наш адрес нет (R-5).
// Неудача не останавливает worker: события догонит сверка, администратор оповещён.
func ensureMAXSubscription(ctx context.Context, cfg Config, in *infra) {
	const op = "Bootstrap.MAXSubscription"
	if cfg.Env != EnvProduction || cfg.MAX.WebhookURL == "" {
		in.log.DebugContext(ctx, "max webhook subscription skipped", logx.KeyOp, op, "env", cfg.Env)
		return
	}
	url := maxWebhookURL(cfg)
	if err := subscribeMAX(ctx, in.max, url, cfg.MAX.WebhookSecret); err != nil {
		in.log.ErrorContext(ctx, "max webhook subscription failed", logx.KeyOp, op, logx.KeyError, err.Error())
		if in.alerter != nil {
			in.alerter.RaiseError(ctx, "max_subscription", "", "Не удалось подписать бота MAX на Webhook", nil)
		}
		return
	}
	in.log.InfoContext(ctx, "max webhook subscription ok", logx.KeyOp, op)
}

// subscribeMAX — подписка, если адреса нет среди текущих подписок бота или подписка на него
// не включает какой-либо нужный вид события (R-17 этапа «Автопостинг и контроль»).
func subscribeMAX(ctx context.Context, api maxAPI, url, secret string) error {
	subs, err := api.ListSubscriptions(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(subs, func(s max.Subscription) bool { return s.URL == url })
	if i >= 0 && !missingTypes(subs[i].UpdateTypes) {
		return nil
	}
	return api.Subscribe(ctx, url, max.ChannelUpdateTypes, secret)
}

// missingTypes — в подписке нет какого-либо вида из max.ChannelUpdateTypes. Пустой список у MAX
// означает «все виды».
func missingTypes(have []string) bool {
	if len(have) == 0 {
		return false
	}
	for _, t := range max.ChannelUpdateTypes {
		if !slices.Contains(have, t) {
			return true
		}
	}
	return false
}
