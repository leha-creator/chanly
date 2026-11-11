// Package app — сценарии модуля каналов (S-12, S-25): коды подключения и операции
// кабинета блогера, разбор событий бота MAX, задания чтения прав, удаления
// сообщения с кодом и выхода из чата. Транзакции, задания, журнал действий и
// журнал приложения — здесь; правила — в domain.
package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// KeyChannelID — поле журнала (07 §5.1).
const KeyChannelID = "channel_id"

// Config — параметры модуля (07 §8); значения по умолчанию — из документов.
type Config struct {
	Visibility domain.VisibilityPolicy
	// LinkCodeTTL — срок кода подключения (A-41: 60 минут).
	LinkCodeTTL time.Duration
	// LinkCodesPerHour — предел кодов организации в час (R-2).
	LinkCodesPerHour int
	// UnclaimedTTL — сколько канал ждёт кода, прежде чем бот уйдёт (A-41: 7 суток).
	UnclaimedTTL time.Duration
	// RknThreshold — порог подписчиков, с которого нужен номер РКН (D-11).
	RknThreshold int
	// Pepper — секрет HMAC кода (R-2); тот же, что у кодов входа.
	Pepper []byte
	// BotName — имя бота платформы в инструкции, например @maxim_ads_bot.
	BotName string
	// EarlyCodeWindow — сколько ждать bot_added для кода в неизвестном канале (R-6).
	EarlyCodeWindow time.Duration
	// EarlyCodeRetry — через сколько повторить разбор такого кода.
	EarlyCodeRetry time.Duration
	// FeeRateBP — комиссия платформы в базисных пунктах (PLATFORM_FEE_RATE_BP, A-30: 1500).
	FeeRateBP int
	// RknRecheckInterval — через сколько подтверждённый номер РКН пора проверить снова (R-17).
	RknRecheckInterval time.Duration
	// Unavailability — пределы интервалов недоступности (R-6).
	Unavailability domain.UnavailabilityLimits
	// Metrics — окна и пороги суточных метрик (R-14, R-15).
	Metrics domain.MetricsConfig
}

func (c *Config) defaults() {
	if c.LinkCodeTTL == 0 {
		c.LinkCodeTTL = time.Hour
	}
	if c.LinkCodesPerHour == 0 {
		c.LinkCodesPerHour = 10
	}
	if c.UnclaimedTTL == 0 {
		c.UnclaimedTTL = 7 * 24 * time.Hour
	}
	if c.RknThreshold == 0 {
		c.RknThreshold = 10000
	}
	if c.EarlyCodeWindow == 0 {
		c.EarlyCodeWindow = 5 * time.Minute
	}
	if c.EarlyCodeRetry == 0 {
		c.EarlyCodeRetry = time.Minute
	}
	if c.RknRecheckInterval == 0 {
		c.RknRecheckInterval = 2160 * time.Hour
	}
	c.Unavailability = c.Unavailability.WithDefaults()
	c.Metrics = c.Metrics.WithDefaults()
}

// Alerter — оповещения администратору (07 §3).
type Alerter interface {
	Raise(ctx context.Context, a alert.Alert) error
}

// Deps — зависимости сервиса; связывает bootstrap. ChatReader и ChatManager нужны
// только в режиме worker.
type Deps struct {
	Connections     domain.Connections
	UserMemberships domain.UserMembershipReader
	Channels        domain.Channels
	LinkCodes       domain.LinkCodes
	Categories      domain.Categories
	// Counter — счётчик каналов на модерации (R-16 этапа «API администратора»); nil — 0.
	Counter interface {
		ModerationCount(ctx context.Context) (int, error)
	}
	Formats domain.Formats
	Prices  domain.Prices
	Unavail domain.Unavailabilities
	Metrics domain.Metrics
	Catalog domain.Catalog
	Admin   domain.AdminChannels
	// Messages — чтение сообщений канала (только worker).
	Messages domain.MessageReader
	// Placements — занятые размещениями интервалы (R-8); nil — пустая реализация.
	Placements domain.PlacementSchedule
	// Cart — признак in_cart каталога; nil — пустая реализация.
	Cart         domain.CartLookup
	Chats        domain.ChatReader
	Bot          domain.ChatManager
	Availability domain.AvailabilityListener
	// PostListener — сообщения каналов для контроля топа; nil — пустая реализация.
	PostListener domain.ChannelPostListener
	// Notifier — уведомления блогеру о смене состояния канала; nil — пустая реализация.
	Notifier domain.Notifier
	Alerts   Alerter
	Tx       tx.Runner
	Jobs     job.Enqueuer
	Audit    audit.Recorder
	Clock    clock.Clock
	Log      *slog.Logger
	Config   Config
}

// Service — сценарии модуля каналов.
type Service struct {
	Deps
	log *slog.Logger
	// approval — модуль маркировки (SetMarkingPorts); nil — маркировка выключена.
	approval domain.ChannelApprovalListener
}

// New создаёт сервис.
func New(d Deps) *Service {
	d.Config.defaults()
	if d.Availability == nil {
		d.Availability = NopAvailability{Log: d.Log}
	}
	if d.Placements == nil {
		d.Placements = NopPlacements{}
	}
	if d.Cart == nil {
		d.Cart = NopCart{}
	}
	if d.PostListener == nil {
		d.PostListener = NopPostListener{Log: d.Log}
	}
	if d.Notifier == nil {
		d.Notifier = NopNotifier{Log: d.Log}
	}
	return &Service{Deps: d, log: d.Log.With(logx.KeyModule, "channel")}
}

// SetPlacementPorts подставляет реализации модуля размещений (bootstrap): модуль
// размещений собирается после модуля каналов, поэтому порты задаются отдельно.
func (s *Service) SetPlacementPorts(availability domain.AvailabilityListener, placements domain.PlacementSchedule, cart domain.CartLookup) {
	s.Availability, s.Placements, s.Cart = availability, placements, cart
}

// SetPostListener подставляет контроль публикаций модуля размещений (R-9; bootstrap).
func (s *Service) SetPostListener(l domain.ChannelPostListener) { s.PostListener = l }

// NopPostListener — пустая реализация ChannelPostListener до модуля размещений.
// NopNotifier — уведомления без модуля уведомлений (тесты модуля).
type NopNotifier struct{ Log *slog.Logger }

func (n NopNotifier) Notify(ctx context.Context, notice domain.Notice) error {
	n.Log.DebugContext(ctx, "notification skipped", logx.KeyOp, "Channel.Notify", "template", notice.Template, KeyChannelID, notice.ChannelID)
	return nil
}

type NopPostListener struct{ Log *slog.Logger }

func (n NopPostListener) ChannelPostCreated(ctx context.Context, p domain.ChannelPost) error {
	n.Log.DebugContext(ctx, "channel post, no placements control yet", logx.KeyOp, "Channel.ChannelPost", KeyChannelID, p.ChannelID)
	return nil
}

// NopAvailability — пустая реализация AvailabilityListener до модуля размещений (R-11).
type NopAvailability struct{ Log *slog.Logger }

func (n NopAvailability) ChannelUnavailable(ctx context.Context, c domain.StatusChange) error {
	n.Log.DebugContext(ctx, "channel availability lost, no placements module yet", logx.KeyOp, "Channel.Availability",
		KeyChannelID, c.ChannelID, "from", c.From, "to", c.To)
	return nil
}

func (n NopAvailability) ChannelRestored(ctx context.Context, c domain.StatusChange) error {
	n.Log.DebugContext(ctx, "channel availability restored, no placements module yet", logx.KeyOp, "Channel.Availability",
		KeyChannelID, c.ChannelID, "from", c.From, "to", c.To)
	return nil
}

// save сохраняет канал и сообщает о смене доступности в той же транзакции (R-11).
// Смена состояния пишется в журнал действий и журнал приложения.
func (s *Service) save(ctx context.Context, op string, c *domain.Channel, change domain.StatusChange) error {
	if err := s.Channels.Save(ctx, c); err != nil {
		if errors.Is(err, domain.ErrStaleVersion) {
			return apperr.Conflict("channel was changed concurrently").WithCause(err)
		}
		return err
	}
	if !change.Changed() {
		return nil
	}
	switch {
	case change.AvailabilityLost():
		if err := s.Availability.ChannelUnavailable(ctx, change); err != nil {
			return err
		}
	case change.AvailabilityRestored():
		if err := s.Availability.ChannelRestored(ctx, change); err != nil {
			return err
		}
	}
	if err := s.Audit.Record(ctx, audit.Entry{
		Action: "channel.status_changed", ObjectType: "channel", ObjectID: c.ID.String(),
		Data:           map[string]any{"from": change.From, "to": change.To, "reason": change.Reason},
		OrganizationID: c.OrganizationID,
	}); err != nil {
		return err
	}
	if err := s.notify(ctx, c, change); err != nil {
		return err
	}
	level := slog.LevelInfo
	if change.AvailabilityLost() {
		level = slog.LevelWarn
	}
	// У rejected причина — текст администратора: в журнал приложения он не пишется (07 §5.4).
	logReason := change.Reason
	if change.To == domain.StatusRejected && logReason != "" {
		logReason = "moderation_rejected"
	}
	s.log.Log(ctx, level, "channel status changed", logx.KeyOp, op, KeyChannelID, c.ID,
		"from", change.From, "to", change.To, "reason", logReason, "missing", c.MissingPermissions())
	return nil
}

func orgOf(c *domain.Channel) uuid.UUID {
	if c.OrganizationID == nil {
		return uuid.Nil
	}
	return *c.OrganizationID
}

// NopPlacements — пустая реализация PlacementSchedule до модуля размещений (R-8).
type NopPlacements struct{}

func (NopPlacements) BusyIntervals(context.Context, uuid.UUID, time.Time, time.Time) ([]domain.Unavailability, error) {
	return nil, nil
}

func (NopPlacements) ScheduledConflict(context.Context, uuid.UUID, time.Time, time.Time) (bool, error) {
	return false, nil
}

// NopCart — пустая реализация CartLookup: каналов в корзине нет.
type NopCart struct{}

func (NopCart) InCart(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error) {
	return map[uuid.UUID]bool{}, nil
}

// ruleError переводит нарушение правила домена в 422 с details.reason (R-4 … R-6, R-10).
func ruleError(err error) error {
	var re *domain.RuleError
	if errors.As(err, &re) {
		return apperr.Validation(re.Field, re.Reason).WithCause(err)
	}
	return err
}

// notEditable — 409 для канала, который блогер не правит в его состоянии.
func notEditable(c *domain.Channel) error {
	return apperr.Conflict("channel cannot be edited in its current status").WithDetail("status", string(c.Status))
}

// notify — уведомление блогеру о смене состояния канала в транзакции смены. Причина отклонения —
// текст администратора для блогера: в уведомление он попадает, в журнал приложения — нет.
func (s *Service) notify(ctx context.Context, c *domain.Channel, change domain.StatusChange) error {
	tpl, ok := change.NoticeTemplate()
	if !ok || c.OrganizationID == nil {
		return nil
	}
	data := map[string]any{"channel_id": c.ID.String()}
	if c.Title != "" {
		data["channel_title"] = c.Title
	}
	if change.To == domain.StatusRejected && change.Reason != "" {
		data["reason"] = change.Reason
	}
	return s.Notifier.Notify(ctx, domain.Notice{Template: tpl, OrganizationID: *c.OrganizationID, ChannelID: c.ID, Data: data})
}
