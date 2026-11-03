package inbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/inbound/sqlc"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Handler обрабатывает событие. Обработчик идемпотентен: событие может прийти
// повторно и задание может выполниться повторно (03 §11). Ошибка — повтор задания.
type Handler func(ctx context.Context, e Event) error

// ErrIgnore — обработчик решил, что событие не требует действий: оно получает статус
// ignored, а не processed (05 §2.2: бот добавлен не в канал, событие по неизвестному каналу).
var ErrIgnore = errors.New("inbound: event ignored by handler")

type route struct{ source, eventType string }

// Dispatcher разбирает события по источнику и виду. Вид без обработчика
// получает статус ignored (05 §2.2: user_added, message_callback и т. п.).
type Dispatcher struct {
	pool     *pgxpool.Pool
	clock    clock.Clock
	log      *slog.Logger
	handlers map[route]Handler
}

// NewDispatcher создаёт Dispatcher без обработчиков.
func NewDispatcher(pool *pgxpool.Pool, clk clock.Clock, log *slog.Logger) *Dispatcher {
	return &Dispatcher{pool: pool, clock: clk, log: log, handlers: map[route]Handler{}}
}

// Handle регистрирует обработчик вида события источника.
func (d *Dispatcher) Handle(source, eventType string, h Handler) {
	d.handlers[route{source, eventType}] = h
}

// Register регистрирует задание process_inbound_event в обработчике заданий.
func (d *Dispatcher) Register(w *job.Worker) {
	w.Register(KindProcessInboundEvent, d.Process, job.HandlerOptions{OnExhausted: d.markFailed})
}

// Process — обработчик задания process_inbound_event.
func (d *Dispatcher) Process(ctx context.Context, j job.Running) error {
	const op = "Inbound.Process"
	var p processPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return job.Permanent(fmt.Errorf("decode payload: %w", err))
	}
	ctx = logx.With(ctx, KeyInboundEventID, p.InboundEventID.String())
	q := sqlc.New(d.pool)
	row, err := q.GetInboundEvent(ctx, p.InboundEventID)
	if errors.Is(err, pgx.ErrNoRows) {
		d.log.WarnContext(ctx, "inbound event not found", logx.KeyOp, op)
		return nil
	}
	if err != nil {
		return fmt.Errorf("get inbound event: %w", err)
	}
	if row.Status != StatusReceived { // идемпотентность: уже разобрано
		d.log.DebugContext(ctx, "inbound event already handled", logx.KeyOp, op, "status", row.Status)
		return nil
	}
	h, ok := d.handlers[route{row.Source, row.EventType}]
	if !ok {
		if err := d.finish(ctx, q, row.ID, StatusIgnored, nil); err != nil {
			return err
		}
		d.log.InfoContext(ctx, "inbound event ignored", logx.KeyOp, op, "source", row.Source, "event_type", row.EventType)
		return nil
	}
	d.log.DebugContext(ctx, "inbound event dispatch", logx.KeyOp, op, "source", row.Source, "event_type", row.EventType)
	err = h(ctx, Event{ID: row.ID, Source: row.Source, ExternalKey: row.ExternalKey, EventType: row.EventType,
		Payload: row.Payload, ReceivedAt: row.CreatedAt})
	if errors.Is(err, ErrIgnore) {
		if err := d.finish(ctx, q, row.ID, StatusIgnored, nil); err != nil {
			return err
		}
		d.log.InfoContext(ctx, "inbound event ignored", logx.KeyOp, op, "source", row.Source, "event_type", row.EventType, "by", "handler")
		return nil
	}
	if err != nil {
		return err
	}
	if err := d.finish(ctx, q, row.ID, StatusProcessed, nil); err != nil {
		return err
	}
	d.log.InfoContext(ctx, "inbound event processed", logx.KeyOp, op, "source", row.Source, "event_type", row.EventType)
	return nil
}

func (d *Dispatcher) finish(ctx context.Context, q *sqlc.Queries, id uuid.UUID, status string, errText *string) error {
	if _, err := q.FinishInboundEvent(ctx, sqlc.FinishInboundEventParams{Status: status, Error: errText, Now: d.clock.Now(), ID: id}); err != nil {
		return fmt.Errorf("finish inbound event: %w", err)
	}
	return nil
}

// markFailed — попытки разбора исчерпаны: событие failed с текстом ошибки.
func (d *Dispatcher) markFailed(ctx context.Context, j job.Running, cause error) {
	var p processPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return
	}
	msg := cause.Error()
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	if err := d.finish(ctx, sqlc.New(d.pool), p.InboundEventID, StatusFailed, &msg); err != nil {
		d.log.ErrorContext(ctx, "inbound event state update failed", logx.KeyOp, "Inbound.Process", logx.KeyError, err.Error())
		return
	}
	d.log.ErrorContext(ctx, "inbound event failed", logx.KeyOp, "Inbound.Process", KeyInboundEventID, p.InboundEventID,
		logx.KeyError, msg)
}

// Handles сообщает, что для вида события источника зарегистрирован обработчик.
func (d *Dispatcher) Handles(source, eventType string) bool {
	_, ok := d.handlers[route{source, eventType}]
	return ok
}
