// Package ratelimit — общий для всех экземпляров ограничитель запросов на
// корзинах токенов в PostgreSQL (05 §2.8, R-1, R-2).
//
// Приоритеты: задания низкого приоритета берут токен, только если после этого
// в корзине остаётся резерв, — так метрики и сверка не задерживают публикацию.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/ratelimit/sqlc"
)

// Priority — приоритет запроса по 05 §2.8, от высшего.
type Priority int

const (
	// PriorityPublish — публикация и снятие.
	PriorityPublish Priority = iota
	// PriorityReply — ответы блогерам, уведомления, оповещения.
	PriorityReply
	// PriorityReconcile — сверка.
	PriorityReconcile
	// PriorityMetrics — метрики каталога.
	PriorityMetrics
)

func (p Priority) String() string {
	switch p {
	case PriorityPublish:
		return "publish"
	case PriorityReply:
		return "reply"
	case PriorityReconcile:
		return "reconcile"
	default:
		return "metrics"
	}
}

// reserveShare — доля ёмкости корзины, которую приоритет оставляет более
// высоким (R-2): при ёмкости 25 это 0, 3, 6 и 10 токенов.
func (p Priority) reserveShare() float64 {
	switch p {
	case PriorityPublish:
		return 0
	case PriorityReply:
		return 0.12
	case PriorityReconcile:
		return 0.24
	default:
		return 0.4
	}
}

// MaxLowPriorityWait — сколько ждут токен приоритеты ниже публикации; затем ErrBusy (R-2).
const MaxLowPriorityWait = 30 * time.Second

// ErrBusy — токен не получен за MaxLowPriorityWait: задание откладывается.
var ErrBusy = errors.New("rate limit busy")

// Bucket — корзина: Rate токенов в секунду, ёмкость Burst.
type Bucket struct {
	Key   string
	Rate  float64
	Burst float64
}

// Limiter выдаёт токены из корзин в базе.
type Limiter struct {
	pool    *pgxpool.Pool
	clock   clock.Clock
	log     *slog.Logger
	sleep   func(ctx context.Context, d time.Duration) error
	metrics *Metrics
}

// Options — необязательные настройки.
type Options struct {
	// Sleep ждёт между попытками; в тестах сдвигает clock.Manual.
	Sleep   func(ctx context.Context, d time.Duration) error
	Metrics *Metrics
}

// New создаёт ограничитель.
func New(pool *pgxpool.Pool, clk clock.Clock, log *slog.Logger, opts Options) *Limiter {
	if opts.Sleep == nil {
		opts.Sleep = sleep
	}
	return &Limiter{pool: pool, clock: clk, log: log, sleep: opts.Sleep, metrics: opts.Metrics}
}

// Wait ждёт токен из корзины b. Публикация ждёт без предела (до отмены ctx),
// остальные приоритеты — до MaxLowPriorityWait.
func (l *Limiter) Wait(ctx context.Context, b Bucket, p Priority) error {
	if b.Rate <= 0 || b.Burst <= 0 {
		return fmt.Errorf("rate limit %s: rate and burst must be positive", b.Key)
	}
	started := l.clock.Now()
	var waited time.Duration
	for {
		taken, wait, err := l.take(ctx, b, p)
		if err != nil {
			return err
		}
		if taken {
			l.metrics.observe(b.Key, p, waited)
			if waited > 0 {
				l.log.DebugContext(ctx, "rate limit wait", logx.KeyOp, "RateLimit.Wait", "key", b.Key,
					"priority", p.String(), "wait_ms", waited.Milliseconds())
			}
			return nil
		}
		if p != PriorityPublish && l.clock.Now().Sub(started)+wait > MaxLowPriorityWait {
			l.log.WarnContext(ctx, "rate limit busy", logx.KeyOp, "RateLimit.Wait", "key", b.Key, "priority", p.String())
			l.metrics.observe(b.Key, p, waited)
			return fmt.Errorf("%s: %w", b.Key, ErrBusy)
		}
		if err := l.sleep(ctx, wait); err != nil {
			return err
		}
		waited += wait
	}
}

// take пытается взять токен; если нельзя — возвращает, сколько ждать.
func (l *Limiter) take(ctx context.Context, b Bucket, p Priority) (bool, time.Duration, error) {
	var taken bool
	var wait time.Duration
	err := pgx.BeginFunc(ctx, l.pool, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		now := l.clock.Now()
		if err := q.EnsureBucket(ctx, sqlc.EnsureBucketParams{Key: b.Key, Tokens: b.Burst, Now: now}); err != nil {
			return err
		}
		row, err := q.LockBucket(ctx, b.Key)
		if err != nil {
			return err
		}
		taken, wait, row.Tokens = decide(row.Tokens, now.Sub(row.RefilledAt), b, p)
		return q.UpdateBucket(ctx, sqlc.UpdateBucketParams{Tokens: row.Tokens, RefilledAt: maxTime(now, row.RefilledAt), Key: b.Key})
	})
	if err != nil {
		return false, 0, fmt.Errorf("rate limit %s: %w", b.Key, err)
	}
	return taken, wait, nil
}

// decide — чистое правило корзины: пополнение за elapsed, списание с учётом
// резерва приоритета. Возвращает, взят ли токен, сколько ждать и новый остаток
// (токен взят — без него).
func decide(tokens float64, elapsed time.Duration, b Bucket, p Priority) (bool, time.Duration, float64) {
	if elapsed < 0 { // часы экземпляров расходятся: не пополнять
		elapsed = 0
	}
	avail := math.Min(b.Burst, tokens+elapsed.Seconds()*b.Rate)
	// Резерв не может сделать токен недостижимым: в корзине ёмкостью 1 (канал) резерва нет.
	need := math.Max(1, math.Min(b.Burst, 1+p.reserveShare()*b.Burst))
	if avail >= need {
		return true, 0, avail - 1
	}
	wait := time.Duration(math.Ceil((need - avail) / b.Rate * float64(time.Second)))
	return false, max(wait, time.Millisecond), avail
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
