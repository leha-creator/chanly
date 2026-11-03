// Package app — сценарии модуля размещений: корзина (S-03), оформление заказа (S-06),
// заказы рекламодателя и отмена (S-07), правка поста после оформления (S-29), действия
// блогера (S-14), таймеры (S-23), последствия версии поста (S-17) и потери канала (S-25).
// Транзакции, задания, журнал действий и журнал приложения — здесь; правила — в domain.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// Поля журнала (07 §5.1).
const (
	KeyPlacementID = "placement_id"
	KeyOrderID     = "order_id"
	KeyChannelID   = "channel_id"
	KeyCampaignID  = "campaign_id"
)

// Попытки заданий (07 §2). У publish_placement и remove_placement предела нет: повторами
// управляет их обработчик (этап «Автопостинг и контроль»).
const (
	timerAttempts     = 5
	checkAttempts     = 3
	unboundedAttempts = 1000
)

// Действия журнала действий.
const (
	actionCartItemAdded        = "cart.item_added"
	actionCartItemUpdated      = "cart.item_updated"
	actionCartItemRemoved      = "cart.item_removed"
	actionOrderCreated         = "order.created"
	actionOrderPostVersion     = "order.post_version_created"
	actionPlacementAccept      = "placement.accept"
	actionPlacementDecline     = "placement.decline"
	actionPlacementSchedule    = "placement.schedule"
	actionPlacementReschedule  = "placement.reschedule"
	actionPlacementCancel      = "placement.cancel"
	defaultPageLimit           = 20
	maxPageLimit               = 100
	holdReleaseOnRejection     = "post_rejected"
	reasonIdempotentRepeat     = "idempotent_repeat"
	versionIgnoredNotCurrent   = "not_current"
	versionIgnoredNoPlacements = "no_order"
)

// Config — параметры модуля (07 §8).
type Config struct {
	// FeeRateBP — комиссия платформы (PLATFORM_FEE_RATE_BP, A-30).
	FeeRateBP int
	Timing    domain.Timing
	Cart      domain.CartRules
	// Control — публикация, снятие и контроль (этап «Автопостинг и контроль»).
	Control domain.ControlRules
}

func (c *Config) defaults() {
	c.Timing = c.Timing.WithDefaults()
	c.Control = c.Control.WithDefaults()
	if c.Cart.MaxItems == 0 {
		c.Cart.MaxItems = 50
	}
}

// Deps — зависимости сервиса; связывает bootstrap.
type Deps struct {
	Carts      domain.Carts
	Orders     domain.Orders
	Placements domain.Placements
	History    domain.History
	// Порты других модулей (переходники в bootstrap).
	Channels  domain.Channels
	Campaigns domain.Campaigns
	Versions  domain.PostVersions
	// Порты будущих модулей (R-2 … R-4).
	Holds       domain.Holds
	Contractors domain.ContractorGate
	// Payouts — создание выплаты при переходе в accepted (этап «Приёмка, споры и возвраты»).
	Payouts  domain.Payouts
	Notifier domain.Notifier
	// Публикации, нарушения, споры и внешние вызовы (этап «Автопостинг и контроль»). Publisher
	// и Content нужны только в режиме worker.
	Publications domain.Publications
	Violations   domain.Violations
	Disputes     domain.Disputes
	// Claims и Ratings — претензии и рейтинг канала (этап «Приёмка, споры и возвраты»).
	Claims    domain.Claims
	Ratings   domain.ChannelRatings
	Publisher domain.ChannelPublisher
	Content   domain.PublicationContent
	// Admin, Messages, Moderation — разделы администратора (этап «API администратора»): выборки,
	// переписка по спору и решение по посту; Moderation — модуль post.
	Admin      domain.AdminReads
	Messages   domain.DisputeMessages
	Moderation domain.PostModeration
	// Alerts — оповещения администратору (07 §3); nil — только журнал.
	Alerts Alerter
	// Metrics — метрики публикации и контроля (07 §5.5); nil — без метрик.
	Metrics Observer
	Tx      tx.Runner
	Jobs    job.Scheduler
	Audit   audit.Recorder
	Clock   clock.Clock
	Log     *slog.Logger
	Config  Config
}

// Service — сценарии модуля размещений.
type Service struct {
	Deps
	log *slog.Logger
}

// New создаёт сервис.
func New(d Deps) *Service {
	d.Config.defaults()
	if d.Metrics == nil {
		d.Metrics = NopObserver{}
	}
	return &Service{Deps: d, log: d.Log.With(logx.KeyModule, "placement")}
}

// Timing — сроки и правила времени модуля.
func (s *Service) Timing() domain.Timing { return s.Config.Timing }

// ruleError переводит отказ домена в ошибку ответа: 422 с details.reason, 409 с причиной
// или кодом отказа оформления, 404.
func ruleError(err error) error {
	var (
		re *domain.RuleError
		ce *domain.ConflictError
		ie *domain.IncompleteError
		cc *domain.CartChangedError
		ib *domain.InsufficientBalanceError
		te *domain.TransitionError
	)
	switch {
	case errors.As(err, &te):
		return apperr.InvalidTransition(string(te.Code), string(te.Status)).WithDetail("reason", te.Reason).WithCause(err)
	case errors.As(err, &re):
		return apperr.Validation(re.Field, re.Reason).WithCause(err)
	case errors.As(err, &ce):
		return apperr.Conflict("action is not possible now").WithDetail("reason", ce.Reason).WithCause(err)
	case errors.As(err, &ie):
		e := apperr.New(apperr.CodeCampaignIncomplete, "campaign is not ready for checkout").WithCause(err)
		if len(ie.MissingFields) > 0 {
			e.WithDetail("missing_fields", ie.MissingFields)
		}
		if len(ie.PostProblems) > 0 {
			e.WithDetail("post_problems", ie.PostProblems)
		}
		return e
	case errors.As(err, &cc):
		items := make([]string, 0, len(cc.Items))
		for _, id := range cc.Items {
			items = append(items, id.String())
		}
		return apperr.New(apperr.CodeCartChanged, "cart has changed").WithDetail("items", items).WithCause(err)
	case errors.As(err, &ib):
		return apperr.New(apperr.CodeInsufficientBalance, "insufficient balance").
			WithDetail("missing_amount", int64(ib.Missing)).WithCause(err)
	case errors.Is(err, domain.ErrChannelNotInCatalog):
		return apperr.NotFound("channel not found")
	}
	return err
}

// rejectReason — машинная причина отказа для журнала.
func rejectReason(err error) string {
	var (
		re *domain.RuleError
		ce *domain.ConflictError
	)
	switch {
	case errors.As(err, &re):
		return re.Reason
	case errors.As(err, &ce):
		return ce.Reason
	}
	if e, ok := apperr.As(ruleError(err)); ok {
		return string(e.Code)
	}
	return "error"
}

// notFound переводит domain.ErrNotFound в 404 с сообщением what.
func notFound(err error, what string) error {
	if errors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound(what + " not found")
	}
	return err
}

// pageLimit приводит размер страницы к допустимому.
func pageLimit(n int) int {
	switch {
	case n <= 0:
		return defaultPageLimit
	case n > maxPageLimit:
		return maxPageLimit
	}
	return n
}

// cursor разбирает курсор страницы; испорченный — 422.
func cursor(s string) (*domain.PageCursor, error) {
	c, err := domain.DecodePageCursor(s)
	if err != nil {
		return nil, ruleError(err)
	}
	return c, nil
}

// keyHash — отпечаток ключа идемпотентности для журнала: сам ключ не пишется.
func keyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}

// repeat пишет повтор действия с тем же ключом (R-18).
func (s *Service) repeat(ctx context.Context, op string, key string, args ...any) {
	s.log.DebugContext(ctx, "idempotent repeat", append([]any{logx.KeyOp, op, "key_hash", keyHash(key)}, args...)...)
}

func uuidPtr(id uuid.UUID) *uuid.UUID { return &id }

// Alerter — оповещения администратору (07 §3).
type Alerter interface {
	Raise(ctx context.Context, a alert.Alert) error
	// Resolve закрывает оповещение по причине и объекту (спор решён).
	Resolve(ctx context.Context, reason, objectKey string) error
}

// Observer — метрики публикации и контроля (07 §5.5, R-15).
type Observer interface {
	// PublishDelay — задержка публикации относительно publish_at.
	PublishDelay(d time.Duration)
	// RemoveDelay — задержка снятия относительно remove_at.
	RemoveDelay(d time.Duration)
	// Violation — найдено нарушение.
	Violation(kind domain.ViolationKind, source domain.ViolationSource)
	// Accepted — размещение принято (P-12, P-13, P-15 с исходом accepted; R-16 этапа приёмки).
	Accepted(code domain.TransitionCode)
	// DisputeOpened — открыт спор: кто открыл и причина.
	DisputeOpened(openedBy domain.OpenedBy, reason string)
	// DisputeResolved — спор решён; took — от открытия до решения.
	DisputeResolved(res domain.Resolution, took time.Duration)
}

// NopObserver — метрики не собираются.
type NopObserver struct{}

func (NopObserver) PublishDelay(time.Duration)                             {}
func (NopObserver) RemoveDelay(time.Duration)                              {}
func (NopObserver) Violation(domain.ViolationKind, domain.ViolationSource) {}
func (NopObserver) Accepted(domain.TransitionCode)                         {}
func (NopObserver) DisputeOpened(domain.OpenedBy, string)                  {}
func (NopObserver) DisputeResolved(domain.Resolution, time.Duration)       {}

// raise поднимает оповещение; ошибка оповещения не отменяет сценарий — она в журнале.
func (s *Service) raise(ctx context.Context, op string, a alert.Alert) {
	if s.Alerts == nil {
		return
	}
	if err := s.Alerts.Raise(ctx, a); err != nil {
		s.log.ErrorContext(ctx, "alert raise failed", logx.KeyOp, op, "reason", a.Reason, logx.KeyError, err.Error())
	}
}

// resolveAlert закрывает оповещение; ошибка не отменяет сценарий — она в журнале.
func (s *Service) resolveAlert(ctx context.Context, op, reason, objectKey string) {
	if s.Alerts == nil {
		return
	}
	if err := s.Alerts.Resolve(ctx, reason, objectKey); err != nil {
		s.log.ErrorContext(ctx, "alert resolve failed", logx.KeyOp, op, "reason", reason, logx.KeyError, err.Error())
	}
}
