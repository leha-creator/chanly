package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Виды заданий суточных метрик (07 §2, R-13, R-20).
const (
	KindScheduleChannelMetrics = "schedule_channel_metrics"
	KindRefreshChannelMetrics  = "refresh_channel_metrics"
	// RefreshMetricsMaxAttempts — попытки refresh_channel_metrics (07 §2).
	RefreshMetricsMaxAttempts = 3
	// metricsScheduleBatch — сколько каналов читать за раз при постановке заданий.
	metricsScheduleBatch = 500
)

// MetricsPayload — полезная нагрузка refresh_channel_metrics.
type MetricsPayload struct {
	ChannelID uuid.UUID `json:"channel_id"`
	// Date — сутки снимка по Москве, YYYY-MM-DD.
	Date string `json:"date"`
}

// metricsStatuses — каналы, которым считаются метрики: moderation — чтобы
// администратор видел их в очереди (S-16 шаг 1, R-13).
func metricsStatus(s domain.Status) bool {
	return s == domain.StatusModeration || s == domain.StatusActive || s == domain.StatusSuspended
}

// enqueueInitialMetrics runs inside the status-change transaction so a newly
// eligible channel does not wait for the next daily scheduler and spread offset.
// The saved version identifies this transition, including reconnects; a separate
// key keeps an already scheduled daily refresh from delaying the first snapshot.
func (s *Service) enqueueInitialMetrics(ctx context.Context, op string, c *domain.Channel, change domain.StatusChange) error {
	// BotAdded can restore suspended before refresh_channel_info reads the bot's
	// rights. Wait for that authoritative read, then refresh on restoration too.
	if !metricsStatus(change.To) || !c.BotIsAdmin || len(c.MissingPermissions()) > 0 ||
		(metricsStatus(change.From) && !change.AvailabilityRestored()) {
		return nil
	}
	now := s.Clock.Now()
	date := domain.MetricsDate(now).Format(time.DateOnly)
	key := fmt.Sprintf("initial_channel_metrics:%s:%d", c.ID, c.LockVersion)
	if err := s.Jobs.Enqueue(ctx, job.Job{
		Kind: KindRefreshChannelMetrics, DedupeKey: key,
		Payload: MetricsPayload{ChannelID: c.ID, Date: date}, RunAt: now, MaxAttempts: RefreshMetricsMaxAttempts,
	}); err != nil {
		s.log.ErrorContext(ctx, "[FIX] initial channel metrics enqueue failed", logx.KeyOp, op,
			KeyChannelID, c.ID, logx.KeyError, err)
		return err
	}
	s.log.InfoContext(ctx, "[FIX] initial channel metrics queued", logx.KeyOp, op,
		KeyChannelID, c.ID, "date", date, "status", c.Status)
	return nil
}

// ScheduleMetrics — задание schedule_channel_metrics (R-13): по каждому каналу ставит
// refresh_channel_metrics со смещением внутри суток по id канала, чтобы запросы к
// MAX шли равномерно (07 §2). Повтор в те же сутки заданий не дублирует (dedupe_key).
func (s *Service) ScheduleMetrics(ctx context.Context) error {
	const op = "Channel.ScheduleMetrics"
	now := s.Clock.Now()
	day := domain.MetricsDate(now)
	dayStart := domain.MetricsDayStart(day)
	date := day.Format(time.DateOnly)
	count := 0
	after := uuid.Nil
	for {
		ids, err := s.Channels.ListForMetrics(ctx, after, metricsScheduleBatch)
		if err != nil {
			return err
		}
		jobs := make([]job.Job, 0, len(ids))
		for _, id := range ids {
			key := "channel_metrics:" + id.String()
			runAt := job.Spread(job.DailyAt(0, 0), key, 24*time.Hour).Next(dayStart.Add(-time.Nanosecond))
			jobs = append(jobs, job.Job{
				Kind: KindRefreshChannelMetrics, DedupeKey: key + ":" + date,
				Payload: MetricsPayload{ChannelID: id, Date: date}, RunAt: runAt, MaxAttempts: RefreshMetricsMaxAttempts,
			})
		}
		if err := s.Jobs.Enqueue(ctx, jobs...); err != nil {
			return err
		}
		count += len(ids)
		if len(ids) < metricsScheduleBatch {
			break
		}
		after = ids[len(ids)-1]
	}
	s.log.InfoContext(ctx, "channel metrics scheduled", logx.KeyOp, op, "count", count, "date", date)
	return nil
}

// RefreshMetrics — задание refresh_channel_metrics (S-32, R-14 … R-16): сведения о
// канале и сообщения из MAX вне транзакции, затем снимок и правило РКН в транзакции.
func (s *Service) RefreshMetrics(ctx context.Context, p MetricsPayload) error {
	const op = "Channel.RefreshMetrics"
	ctx = logx.With(ctx, KeyChannelID, p.ChannelID.String())
	_, err := time.Parse(time.DateOnly, p.Date)
	if err != nil {
		return job.Permanent(fmt.Errorf("refresh metrics: bad date %q: %w", p.Date, err))
	}
	var chatID int64
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Channels.GetForUpdate(ctx, p.ChannelID)
		if err != nil {
			return err
		}
		if metricsStatus(c.Status) {
			chatID = c.MaxChatID
		} else {
			s.log.InfoContext(ctx, "channel metrics skipped", logx.KeyOp, op, "status", c.Status)
		}
		return nil
	})
	if errors.Is(err, domain.ErrNotFound) {
		return job.Permanent(fmt.Errorf("channel %s not found", p.ChannelID))
	}
	if err != nil || chatID == 0 {
		return err
	}

	// Timestamp the read before external calls. A slower older request must not
	// overwrite a newer checkpoint after acquiring the channel lock.
	observedAt := s.Clock.Now()
	day := domain.MetricsDate(observedAt)
	info, err := s.Chats.GetChat(ctx, chatID)
	if job.IsPermanent(err) {
		// R-20: канала нет в MAX (404) — без повторов; канал отключат события бота.
		s.log.WarnContext(ctx, "channel metrics failed permanently", logx.KeyOp, op, logx.KeyError, err.Error())
	}
	if err != nil {
		return err
	}
	msgs, complete, err := s.readMessages(ctx, op, chatID, observedAt)
	if err != nil {
		return err
	}

	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Channels.GetForUpdate(ctx, p.ChannelID)
		if err != nil {
			return err
		}
		if !metricsStatus(c.Status) {
			// Канал отключили, пока шло чтение: прочитанное не сохраняется.
			s.log.InfoContext(ctx, "channel metrics skipped", logx.KeyOp, op, "status", c.Status)
			return nil
		}
		checkpoint, err := s.Metrics.Checkpoint(ctx, c.ID)
		if err != nil {
			return err
		}
		if checkpoint != nil && !observedAt.After(checkpoint.ObservedAt) {
			s.log.DebugContext(ctx, "[FIX] stale channel metrics sample ignored", logx.KeyOp, op, "observed_at", observedAt)
			return nil
		}
		now := s.Clock.Now()
		prev, err := s.Metrics.LastBefore(ctx, c.ID, day)
		if err != nil {
			return err
		}
		latest, err := s.Metrics.Recent(ctx, c.ID, 1)
		if err != nil {
			return err
		}
		var daily *domain.MetricsSnapshot
		if len(latest) > 0 && latest[0].Date.Equal(day) {
			daily = &latest[0]
		}
		growth := domain.ComputeViewsGrowth(checkpoint, daily, msgs, observedAt, complete, s.Config.Metrics.Window)
		snap := domain.ComputeMetrics(info.SubscribersCount, msgs, prev, observedAt, s.Config.Metrics)
		snap.Date = day
		snap.ObservedAt, snap.NewViews, snap.ViewsPeriodStartedAt = &observedAt, growth.NewViews, growth.PeriodStart
		if err := s.Metrics.Upsert(ctx, c.ID, snap, now); err != nil {
			return err
		}
		if err := s.Metrics.RecordViews(ctx, c.ID, growth.Checkpoint); err != nil {
			return err
		}
		change := c.MetricsRefreshed(info, s.Config.RknThreshold, now)
		if err := s.save(ctx, op, c, change); err != nil {
			return err
		}
		if change.Changed() && change.Reason == domain.ReasonRknRequired {
			s.log.WarnContext(ctx, "channel rkn required", logx.KeyOp, op, "subscribers_count", c.SubscribersCount)
		}
		if len(snap.AnomalyFlags) > 0 {
			s.log.WarnContext(ctx, "channel anomaly detected", logx.KeyOp, op, "flags", snap.AnomalyFlags)
		}
		s.log.InfoContext(ctx, "channel metrics refreshed", logx.KeyOp, op, "subscribers_count", snap.SubscribersCount,
			"avg_views", snap.AvgViews, "new_views", snap.NewViews, "observed_at", observedAt,
			"anomaly_flags", snap.AnomalyFlags, "messages", len(msgs))
		return nil
	})
}

// readMessages читает сообщения канала за окно метрик страницами по 100, новые первыми,
// не больше MaxPages страниц (R-14).
func (s *Service) readMessages(ctx context.Context, op string, chatID int64, now time.Time) ([]domain.MessageStat, bool, error) {
	cfg := s.Config.Metrics
	from, to := now.Add(-cfg.Window), now
	var out []domain.MessageStat
	for page := 1; page <= cfg.MaxPages; page++ {
		list, err := s.Messages.ListMessages(ctx, chatID, from, to, cfg.PageSize())
		if err != nil {
			return nil, false, err
		}
		out = append(out, list...)
		if len(list) < cfg.PageSize() {
			return out, true, nil
		}
		oldest := list[len(list)-1].At
		for _, m := range list {
			if m.At.Before(oldest) {
				oldest = m.At
			}
		}
		if !oldest.After(from) {
			return out, true, nil
		}
		to = oldest.Add(-time.Millisecond)
	}
	s.log.WarnContext(ctx, "channel metrics window truncated", logx.KeyOp, op, "pages", cfg.MaxPages, "messages", len(out))
	return out, false, nil
}
