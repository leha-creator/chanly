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
	day, err := time.Parse(time.DateOnly, p.Date)
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

	info, err := s.Chats.GetChat(ctx, chatID)
	if job.IsPermanent(err) {
		// R-20: канала нет в MAX (404) — без повторов; канал отключат события бота.
		s.log.WarnContext(ctx, "channel metrics failed permanently", logx.KeyOp, op, logx.KeyError, err.Error())
	}
	if err != nil {
		return err
	}
	msgs, err := s.readMessages(ctx, op, chatID)
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
		now := s.Clock.Now()
		prev, err := s.Metrics.LastBefore(ctx, c.ID, day)
		if err != nil {
			return err
		}
		snap := domain.ComputeMetrics(info.SubscribersCount, msgs, prev, now, s.Config.Metrics)
		snap.Date = day
		if err := s.Metrics.Upsert(ctx, c.ID, snap, now); err != nil {
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
			"avg_views", snap.AvgViews, "anomaly_flags", snap.AnomalyFlags, "messages", len(msgs))
		return nil
	})
}

// readMessages читает сообщения канала за окно метрик страницами по 100, новые первыми,
// не больше MaxPages страниц (R-14).
func (s *Service) readMessages(ctx context.Context, op string, chatID int64) ([]domain.MessageStat, error) {
	cfg := s.Config.Metrics
	now := s.Clock.Now()
	from, to := now.Add(-cfg.Window), now
	var out []domain.MessageStat
	for page := 1; page <= cfg.MaxPages; page++ {
		list, err := s.Messages.ListMessages(ctx, chatID, from, to, cfg.PageSize())
		if err != nil {
			return nil, err
		}
		out = append(out, list...)
		if len(list) < cfg.PageSize() {
			return out, nil
		}
		oldest := list[len(list)-1].At
		for _, m := range list {
			if m.At.Before(oldest) {
				oldest = m.At
			}
		}
		if !oldest.After(from) {
			return out, nil
		}
		to = oldest.Add(-time.Millisecond)
	}
	s.log.WarnContext(ctx, "channel metrics window truncated", logx.KeyOp, op, "pages", cfg.MaxPages, "messages", len(out))
	return out, nil
}
