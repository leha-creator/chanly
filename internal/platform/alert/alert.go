// Package alert — оповещения администратору (07 §3, R-4, R-5).
//
// Raise записывает причину в таблицу alert; не чаще раза в интервал (час) по
// одной паре «причина + объект» ставит задания send_alert — по одному на канал
// и адресата, чтобы повтор не дублировал уже доставленное. Каналы — почта
// (ALERT_EMAILS) и чат дежурных в MAX (ALERT_MAX_CHAT_ID).
package alert

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/alert/sqlc"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// Уровни (07 §3: WARN и ERROR).
const (
	LevelWarn  = "warn"
	LevelError = "error"
)

// Каналы доставки.
const (
	ChannelEmail = "email"
	ChannelMAX   = "max"
)

// Задание отправки (R-11).
const (
	KindSendAlert       = "send_alert"
	sendMaxAttempts     = 5
	defaultSendInterval = time.Hour
)

// Alert — причина оповещения. Summary и Fields — без персональных данных и
// секретов (07 §5.4): только коды, идентификаторы, счётчики.
type Alert struct {
	// Reason — код причины: job_failed:<kind>, circuit_open:<system>, downtime, stuck:<check>…
	Reason string
	// ObjectKey — объект причины; пусто — причина целиком.
	ObjectKey string
	Level     string
	Summary   string
	Fields    map[string]any
}

// Options — адресаты и интервал повторной отправки.
type Options struct {
	Emails []string
	// ChatID — чат дежурных в MAX; 0 — не задан.
	ChatID int64
	// Interval — не чаще одного оповещения по причине за интервал (07 §3: час).
	Interval time.Duration
}

// Alerter поднимает и закрывает оповещения.
type Alerter struct {
	pool  *pgxpool.Pool
	tx    tx.Runner
	jobs  job.Enqueuer
	clock clock.Clock
	log   *slog.Logger
	opts  Options
}

// New создаёт Alerter.
func New(pool *pgxpool.Pool, txr tx.Runner, jobs job.Enqueuer, clk clock.Clock, log *slog.Logger, opts Options) *Alerter {
	if opts.Interval <= 0 {
		opts.Interval = defaultSendInterval
	}
	return &Alerter{pool: pool, tx: txr, jobs: jobs, clock: clk, log: log, opts: opts}
}

// CheckChannels пишет WARN о незаданных каналах (вызывается при запуске worker).
func (a *Alerter) CheckChannels(ctx context.Context) {
	if len(a.opts.Emails) == 0 {
		a.log.WarnContext(ctx, "alert channel not configured", logx.KeyOp, "Alert.CheckChannels", "channel", ChannelEmail)
	}
	if a.opts.ChatID == 0 {
		a.log.WarnContext(ctx, "alert channel not configured", logx.KeyOp, "Alert.CheckChannels", "channel", ChannelMAX)
	}
}

// sendPayload — полезная нагрузка send_alert: один канал, один адресат.
type sendPayload struct {
	AlertID string         `json:"alert_id"`
	Channel string         `json:"channel"`
	To      string         `json:"to"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// Raise записывает оповещение и, если по причине давно не отправляли, ставит
// отправку. Запись не зависит от транзакции вызывающего: её откат оповещение
// не отменяет.
func (a *Alerter) Raise(ctx context.Context, al Alert) error {
	const op = "Alert.Raise"
	if al.Level != LevelWarn {
		al.Level = LevelError
	}
	ctx = tx.Detach(ctx)
	var raisedCount int32
	var sent bool
	err := a.tx.InTx(ctx, func(ctx context.Context) error {
		q := sqlc.New(db.Conn(ctx, a.pool))
		now := a.clock.Now()
		row, err := q.UpsertAlert(ctx, sqlc.UpsertAlertParams{
			ID: ids.New(), Reason: al.Reason, ObjectKey: al.ObjectKey, Level: al.Level, Summary: al.Summary, Now: now,
		})
		if err != nil {
			return fmt.Errorf("upsert alert: %w", err)
		}
		raisedCount = row.RaisedCount
		n, err := q.ClaimAlertSend(ctx, sqlc.ClaimAlertSendParams{Now: now, ID: row.ID, SentBefore: now.Add(-a.opts.Interval)})
		if err != nil {
			return fmt.Errorf("claim alert send: %w", err)
		}
		if n == 0 {
			return nil
		}
		sent = true
		return a.jobs.Enqueue(ctx, a.sendJobs(row.ID.String(), al.Fields)...)
	})
	if err != nil {
		a.log.ErrorContext(ctx, "alert raise failed", logx.KeyOp, op, "reason", al.Reason, logx.KeyError, err.Error())
		return err
	}
	if !sent {
		a.log.DebugContext(ctx, "alert suppressed", logx.KeyOp, op, "reason", al.Reason, "object_key", al.ObjectKey,
			"raised_count", raisedCount)
		return nil
	}
	level := slog.LevelError
	if al.Level == LevelWarn {
		level = slog.LevelWarn
	}
	a.log.Log(ctx, level, "alert raised", logx.KeyOp, op, "reason", al.Reason, "object_key", al.ObjectKey,
		"raised_count", raisedCount)
	return nil
}

func (a *Alerter) sendJobs(alertID string, fields map[string]any) []job.Job {
	var jobs []job.Job
	// В dedupe_key — номер адресата, а не адрес: ключ попадает в журнал (07 §5.4).
	add := func(channel, to string, n int) {
		jobs = append(jobs, job.Job{
			Kind: KindSendAlert, DedupeKey: "alert:" + alertID + ":" + channel + ":" + strconv.Itoa(n), MaxAttempts: sendMaxAttempts,
			Payload: sendPayload{AlertID: alertID, Channel: channel, To: to, Fields: fields},
		})
	}
	for i, e := range a.opts.Emails {
		add(ChannelEmail, e, i)
	}
	if a.opts.ChatID != 0 {
		add(ChannelMAX, strconv.FormatInt(a.opts.ChatID, 10), 0)
	}
	return jobs
}

// RaiseError — оповещение уровня ERROR; ошибка записи только журналируется.
// Реализует job.Alerter и outbound.Alerter.
func (a *Alerter) RaiseError(ctx context.Context, reason, objectKey, summary string, fields map[string]any) {
	_ = a.Raise(ctx, Alert{Reason: reason, ObjectKey: objectKey, Level: LevelError, Summary: summary, Fields: fields})
}

// RaiseWarn — оповещение уровня WARN.
func (a *Alerter) RaiseWarn(ctx context.Context, reason, objectKey, summary string, fields map[string]any) {
	_ = a.Raise(ctx, Alert{Reason: reason, ObjectKey: objectKey, Level: LevelWarn, Summary: summary, Fields: fields})
}

// Resolve закрывает оповещение: причина устранена.
func (a *Alerter) Resolve(ctx context.Context, reason, objectKey string) error {
	n, err := sqlc.New(a.pool).ResolveAlert(tx.Detach(ctx), sqlc.ResolveAlertParams{Now: a.clock.Now(), Reason: reason, ObjectKey: objectKey})
	if err != nil {
		return fmt.Errorf("resolve alert: %w", err)
	}
	if n > 0 {
		a.log.InfoContext(ctx, "alert resolved", logx.KeyOp, "Alert.Resolve", "reason", reason, "object_key", objectKey)
	}
	return nil
}

// OpenKeys — объекты открытых оповещений причины.
func (a *Alerter) OpenKeys(ctx context.Context, reason string) ([]string, error) {
	keys, err := sqlc.New(a.pool).OpenAlertKeys(ctx, reason)
	if err != nil {
		return nil, fmt.Errorf("open alert keys: %w", err)
	}
	return keys, nil
}
