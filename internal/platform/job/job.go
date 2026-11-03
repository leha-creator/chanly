// Package job — очередь заданий на таблице job (07 §2, 04 §8).
//
// Задание создаётся в транзакции из context — вместе с изменением, которое его
// породило (I-10). Каждое задание выполняется не менее одного раза, поэтому
// обработчики идемпотентны (03 §11).
package job

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job/sqlc"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// requestIDKey — поле полезной нагрузки со сквозным идентификатором (07 §5.1).
const requestIDKey = "_request_id"

// Job — задание к постановке в очередь.
type Job struct {
	Kind string
	// DedupeKey не даёт создать второе живое задание того же назначения.
	DedupeKey string
	// Payload сериализуется в JSON-объект.
	Payload any
	// RunAt — когда выполнить; нулевое значение — сразу.
	RunAt       time.Time
	MaxAttempts int
}

// Enqueuer ставит задания в очередь. Слой app зависит только от этого интерфейса.
type Enqueuer interface {
	Enqueue(ctx context.Context, jobs ...Job) error
}

// Scheduler — постановка, отмена и перенос заданий по dedupe_key. Всё работает
// в транзакции из context (I-10).
type Scheduler interface {
	Enqueuer
	// Cancel отменяет ожидающее задание; false — живого ожидающего задания нет.
	Cancel(ctx context.Context, dedupeKey string) (bool, error)
	// Reschedule переносит ожидающее задание на runAt; false — задания нет.
	Reschedule(ctx context.Context, dedupeKey string, runAt time.Time) (bool, error)
	// Exists — есть живое (ожидающее или выполняемое) задание с dedupeKey.
	Exists(ctx context.Context, dedupeKey string) (bool, error)
}

// Queue — запись заданий в таблицу job.
type Queue struct {
	pool  *pgxpool.Pool
	clock clock.Clock
	log   *slog.Logger
}

// NewQueue создаёт очередь.
func NewQueue(pool *pgxpool.Pool, clk clock.Clock, log *slog.Logger) *Queue {
	return &Queue{pool: pool, clock: clk, log: log}
}

// Enqueue пишет задания в транзакции из context (или без неё). Повтор живого
// задания с тем же DedupeKey не создаёт второе и ошибкой не считается.
func (q *Queue) Enqueue(ctx context.Context, jobs ...Job) error {
	queries := sqlc.New(db.Conn(ctx, q.pool))
	now := q.clock.Now()
	for _, j := range jobs {
		if j.Kind == "" || j.MaxAttempts < 1 {
			return fmt.Errorf("enqueue %q: kind and max attempts are required", j.Kind)
		}
		payload, err := encodePayload(j.Payload, logx.RequestID(ctx))
		if err != nil {
			return fmt.Errorf("enqueue %s: %w", j.Kind, err)
		}
		runAt := j.RunAt
		if runAt.IsZero() {
			runAt = now
		}
		var dedupe *string
		if j.DedupeKey != "" {
			dedupe = &j.DedupeKey
		}
		id := ids.New()
		n, err := queries.InsertJob(ctx, sqlc.InsertJobParams{
			ID: id, Kind: j.Kind, DedupeKey: dedupe, Payload: payload, RunAt: runAt,
			MaxAttempts: int32(j.MaxAttempts), Now: now,
		})
		if err != nil {
			return fmt.Errorf("enqueue %s: %w", j.Kind, err)
		}
		if n == 0 {
			q.log.DebugContext(ctx, "job already enqueued", logx.KeyOp, "Job.Enqueue", "kind", j.Kind, "dedupe_key", j.DedupeKey)
			continue
		}
		q.log.DebugContext(ctx, "job enqueued", logx.KeyOp, "Job.Enqueue", logx.KeyJobID, id, "kind", j.Kind,
			"dedupe_key", j.DedupeKey, "run_at", runAt)
	}
	return nil
}

// Cancel переводит ожидающее задание с dedupeKey в cancelled. Задание в running
// не отменяется: его обработчик сам проверит состояние (03 §11).
func (q *Queue) Cancel(ctx context.Context, dedupeKey string) (bool, error) {
	n, err := sqlc.New(db.Conn(ctx, q.pool)).CancelJob(ctx, sqlc.CancelJobParams{Now: q.clock.Now(), DedupeKey: &dedupeKey})
	if err != nil {
		return false, fmt.Errorf("cancel job %s: %w", dedupeKey, err)
	}
	if n == 0 {
		q.log.DebugContext(ctx, "job to cancel not found", logx.KeyOp, "Job.Cancel", "dedupe_key", dedupeKey)
		return false, nil
	}
	q.log.InfoContext(ctx, "job cancelled", logx.KeyOp, "Job.Cancel", "dedupe_key", dedupeKey)
	return true, nil
}

// Reschedule переносит ожидающее задание с dedupeKey на runAt.
func (q *Queue) Reschedule(ctx context.Context, dedupeKey string, runAt time.Time) (bool, error) {
	n, err := sqlc.New(db.Conn(ctx, q.pool)).RescheduleJob(ctx, sqlc.RescheduleJobParams{
		RunAt: runAt, Now: q.clock.Now(), DedupeKey: &dedupeKey,
	})
	if err != nil {
		return false, fmt.Errorf("reschedule job %s: %w", dedupeKey, err)
	}
	if n == 0 {
		q.log.DebugContext(ctx, "job to reschedule not found", logx.KeyOp, "Job.Reschedule", "dedupe_key", dedupeKey)
		return false, nil
	}
	q.log.DebugContext(ctx, "job rescheduled", logx.KeyOp, "Job.Reschedule", "dedupe_key", dedupeKey, "run_at", runAt)
	return true, nil
}

// Exists сообщает, что есть живое задание с dedupeKey (pending или running).
func (q *Queue) Exists(ctx context.Context, dedupeKey string) (bool, error) {
	ok, err := sqlc.New(db.Conn(ctx, q.pool)).LiveJobExists(ctx, &dedupeKey)
	if err != nil {
		return false, fmt.Errorf("job exists %s: %w", dedupeKey, err)
	}
	return ok, nil
}

// encodePayload сериализует полезную нагрузку и добавляет сквозной идентификатор.
func encodePayload(p any, requestID string) ([]byte, error) {
	fields := map[string]json.RawMessage{}
	if p != nil {
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, fmt.Errorf("marshal payload: %w", err)
		}
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("payload must be a JSON object: %w", err)
		}
	}
	if requestID != "" {
		id, _ := json.Marshal(requestID)
		fields[requestIDKey] = id
	}
	return json.Marshal(fields)
}

// decodePayload отделяет сквозной идентификатор от полезной нагрузки.
func decodePayload(raw []byte) (payload []byte, requestID string) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return raw, ""
	}
	if v, ok := fields[requestIDKey]; ok {
		_ = json.Unmarshal(v, &requestID)
		delete(fields, requestIDKey)
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return raw, requestID
	}
	return out, requestID
}
