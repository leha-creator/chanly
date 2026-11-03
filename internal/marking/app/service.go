// Package app — сценарии модуля маркировки (05 §3, S-16 шаг 3, S-17 шаг 3, S-20, S-28):
// контрагенты, договоры, площадки и креативы в ОРД, erid версии поста, опрос статусов,
// ошибки и повтор. Транзакции, задания, журнал действий и журнал приложения — здесь;
// правила — в domain. Вызовы ОРД — только из обработчиков заданий вне транзакции (I-13).
package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// Виды заданий (07 §2).
const (
	KindSyncOrdObject = "sync_ord_object"
	KindPollOrdStatus = "poll_ord_status"
	// SyncAttempts — max_attempts sync_ord_object.
	SyncAttempts = 10
)

// Поля журнала (07 §5.1, обязательные поля этапа).
const (
	KeyObjectType   = "ord_object_type"
	KeyObjectID     = "ord_object_id"
	KeyReqID        = "reqid"
	KeyOrdStatus    = "ord_status"
	KeyCreativeID   = "creative_id"
	KeyVersionID    = "post_version_id"
	KeyChannelID    = "channel_id"
	reasonNew       = "new"
	reasonChanged   = "changed"
	reasonRetry     = "retry"
	reasonDependant = "dependency"
)

// Причины оповещений (R-17).
const (
	alertSyncFailed       = "ord_sync_failed:"
	alertCreativeRejected = "ord_creative_rejected"
	alertEridOverdue      = "post_version_erid_overdue"
	alertSentOverdue      = "ord_object_sent_overdue"
	alertEridAfterReady   = "post_version_ready_failed"
)

// SyncPayload — полезная нагрузка sync_ord_object и poll_ord_status.
type SyncPayload struct {
	Ref string `json:"ref"`
}

// SyncDedupeKey — dedupe_key задания отправки объекта.
func SyncDedupeKey(ref domain.ObjectRef) string { return "ord_sync:" + ref.String() }

// PollDedupeKey — dedupe_key задания опроса статуса (R-10).
func PollDedupeKey(ref domain.ObjectRef) string { return "ord_status:" + ref.String() }

// Alerter — оповещения администратору (07 §3).
type Alerter interface {
	Raise(ctx context.Context, a alert.Alert) error
}

// Observer — метрики маркировки (07 §5.5).
type Observer interface {
	// EridWait — время от одобрения версии (создания креатива) до erid.
	EridWait(d time.Duration)
}

// NopObserver — метрики не собираются.
type NopObserver struct{}

func (NopObserver) EridWait(time.Duration) {}

// Deps — зависимости сервиса; связывает bootstrap.
type Deps struct {
	Organizations domain.OrdOrganizations
	Contracts     domain.OrdContracts
	Platforms     domain.OrdPlatforms
	Creatives     domain.Creatives
	Overview      domain.Overview
	// Reports — ежемесячная отчётность (этап «API администратора и отчётность ОРД»); nil — не подключена.
	Reports domain.Reports
	// Source — размещения для отчёта (переходник над placement); nil — отчётность не подключена.
	Source domain.ReportSource
	// Порты других модулей (переходники в bootstrap/bridge).
	Accounts domain.Organizations
	Posts    domain.CreativeSource
	Versions domain.VersionMarker
	Channels domain.ChannelPlatformReady
	// Listener — модуль размещений при отклонении креатива ЕРИР (R-12); nil — без него.
	Listener domain.CreativeListener
	// Ord — Яндекс ОРД; нужен только в режиме worker.
	Ord     domain.OrdAPI
	Alerts  Alerter
	Metrics Observer
	Tx      tx.Runner
	Jobs    job.Enqueuer
	Audit   audit.Recorder
	Clock   clock.Clock
	Log     *slog.Logger
	Config  domain.Config
}

// Service — сценарии модуля маркировки.
type Service struct {
	Deps
	log *slog.Logger
}

// New создаёт сервис.
func New(d Deps) *Service {
	d.Config = d.Config.WithDefaults()
	if d.Metrics == nil {
		d.Metrics = NopObserver{}
	}
	if d.Listener == nil {
		d.Listener = domain.NopCreativeListener{}
	}
	return &Service{Deps: d, log: d.Log.With(logx.KeyModule, "marking")}
}

// SetCreativeListener подставляет модуль размещений (bootstrap): тот собирается позже.
func (s *Service) SetCreativeListener(l domain.CreativeListener) {
	if l == nil {
		l = domain.NopCreativeListener{}
	}
	s.Listener = l
}

// raise поднимает оповещение; ошибка оповещения не отменяет сценарий — она в журнале.
func (s *Service) raise(ctx context.Context, op string, a alert.Alert) {
	if s.Alerts == nil {
		return
	}
	if err := s.Alerts.Raise(ctx, a); err != nil {
		s.log.ErrorContext(ctx, "alert raise failed", logx.KeyOp, op, "reason", a.Reason, logx.KeyError, err.Error())
	}
}

// enqueueSync ставит отправку объекта в транзакции вызывающего.
func (s *Service) enqueueSync(ctx context.Context, op string, ref domain.ObjectRef, reason string) error {
	if err := s.Jobs.Enqueue(ctx, job.Job{
		Kind: KindSyncOrdObject, DedupeKey: SyncDedupeKey(ref), MaxAttempts: SyncAttempts,
		Payload: SyncPayload{Ref: ref.String()},
	}); err != nil {
		return err
	}
	s.log.DebugContext(ctx, "ord object queued", logx.KeyOp, op, KeyObjectType, ref.Type, KeyObjectID, ref.ID, "reason", reason)
	return nil
}

// enqueuePoll ставит опрос статуса в транзакции перехода в sent (R-10).
func (s *Service) enqueuePoll(ctx context.Context, ref domain.ObjectRef, at time.Time) error {
	return s.Jobs.Enqueue(ctx, job.Job{
		Kind: KindPollOrdStatus, DedupeKey: PollDedupeKey(ref), MaxAttempts: job.Unlimited, RunAt: at,
		Payload: SyncPayload{Ref: ref.String()},
	})
}
