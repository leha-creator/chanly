// Package inbound — входящие события внешних систем (05 §2.2, 04 §8).
//
// Приём (httpapi модуля) проверяет подлинность и вызывает Recorder.Record: событие
// сохраняется с уникальным ключом и ставится задание process_inbound_event в той же
// транзакции. Ответ 200 отдаётся сразу, разбор выполняет Dispatcher в задании.
package inbound

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/inbound/sqlc"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// Источники событий (inbound_event.source).
const (
	SourceMAX        = "max"
	SourceRocketWork = "rocketwork"
)

// Статусы (04 §9).
const (
	StatusReceived  = "received"
	StatusProcessed = "processed"
	StatusIgnored   = "ignored"
	StatusFailed    = "failed"
)

// Задание разбора события (07 §2).
const (
	KindProcessInboundEvent = "process_inbound_event"
	processMaxAttempts      = 10
)

// KeyInboundEventID — поле журнала (07 §5.1).
const KeyInboundEventID = "inbound_event_id"

// Event — входящее событие.
type Event struct {
	ID          uuid.UUID
	Source      string
	ExternalKey string
	EventType   string
	// Payload — сырое тело (JSON).
	Payload []byte
	// ReceivedAt — когда событие принято; заполняется при разборе.
	ReceivedAt time.Time
}

// KeyFromBody — ключ события, если внешняя система его не передаёт: SHA-256 от тела (05 §2.2).
func KeyFromBody(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Recorder сохраняет события и ставит их разбор.
type Recorder struct {
	pool  *pgxpool.Pool
	tx    tx.Runner
	jobs  job.Enqueuer
	clock clock.Clock
	log   *slog.Logger
}

// NewRecorder создаёт Recorder.
func NewRecorder(pool *pgxpool.Pool, txr tx.Runner, jobs job.Enqueuer, clk clock.Clock, log *slog.Logger) *Recorder {
	return &Recorder{pool: pool, tx: txr, jobs: jobs, clock: clk, log: log}
}

type processPayload struct {
	InboundEventID uuid.UUID `json:"inbound_event_id"`
}

// Record сохраняет событие и в той же транзакции ставит задание разбора.
// duplicate — событие с тем же ключом уже было: ничего не сделано (05 §2.2, шаг 3).
func (r *Recorder) Record(ctx context.Context, e Event) (duplicate bool, err error) {
	const op = "Inbound.Record"
	if e.Source == "" || e.ExternalKey == "" || e.EventType == "" {
		return false, errors.New("inbound event: source, external key and event type are required")
	}
	if !json.Valid(e.Payload) {
		return false, errors.New("inbound event: payload must be JSON")
	}
	id := ids.New()
	err = r.tx.InTx(ctx, func(ctx context.Context) error {
		n, err := sqlc.New(db.Conn(ctx, r.pool)).InsertInboundEvent(ctx, sqlc.InsertInboundEventParams{
			ID: id, Source: e.Source, ExternalKey: e.ExternalKey, EventType: e.EventType, Payload: e.Payload, Now: r.clock.Now(),
		})
		if err != nil {
			return fmt.Errorf("insert inbound event: %w", err)
		}
		if n == 0 {
			duplicate = true
			return nil
		}
		return r.jobs.Enqueue(ctx, job.Job{
			Kind: KindProcessInboundEvent, DedupeKey: "inbound:" + id.String(),
			Payload: processPayload{InboundEventID: id}, MaxAttempts: processMaxAttempts,
		})
	})
	if err != nil {
		r.log.ErrorContext(ctx, "inbound event record failed", logx.KeyOp, op, "source", e.Source,
			"event_type", e.EventType, logx.KeyError, err.Error())
		return false, err
	}
	if duplicate {
		r.log.DebugContext(ctx, "inbound event duplicate", logx.KeyOp, op, "source", e.Source,
			"event_type", e.EventType, "external_key", e.ExternalKey)
		return true, nil
	}
	r.log.InfoContext(ctx, "inbound event accepted", logx.KeyOp, op, "source", e.Source,
		"event_type", e.EventType, KeyInboundEventID, id)
	return false, nil
}
