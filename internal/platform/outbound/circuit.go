package outbound

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound/sqlc"
)

// Предохранитель 05 §1: после 10 ошибок подряд по системе вызовы
// приостанавливаются на 60 с. Состояние — в outbound_circuit, общее для всех
// экземпляров (R-3).
const (
	circuitThreshold = 10
	circuitOpenFor   = 60 * time.Second
)

type circuit struct {
	pool    *pgxpool.Pool
	clock   clock.Clock
	log     *slog.Logger
	system  string
	alerter Alerter
	metrics *Metrics
}

// check сообщает, открыт ли предохранитель, и сколько ждать.
func (c circuit) check(ctx context.Context) (time.Duration, bool, error) {
	row, err := sqlc.New(c.pool).GetCircuit(ctx, c.system)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read circuit %s: %w", c.system, err)
	}
	now := c.clock.Now()
	if row.OpenUntil != nil && row.OpenUntil.After(now) {
		c.metrics.setCircuit(c.system, true)
		return row.OpenUntil.Sub(now), true, nil
	}
	c.metrics.setCircuit(c.system, false)
	return 0, false, nil
}

// failure засчитывает сбой системы. opened — предохранитель открылся этим сбоем.
func (c circuit) failure(ctx context.Context) (time.Duration, bool) {
	now := c.clock.Now()
	row, err := sqlc.New(c.pool).RecordCircuitFailure(context.WithoutCancel(ctx), sqlc.RecordCircuitFailureParams{
		System: c.system, Threshold: circuitThreshold, OpenUntil: now.Add(circuitOpenFor), Now: now,
	})
	if err != nil {
		c.log.ErrorContext(ctx, "circuit update failed", logx.KeyOp, "Outbound.Circuit", "system", c.system, logx.KeyError, err.Error())
		return 0, false
	}
	if row.ConsecutiveFailures < circuitThreshold || row.OpenUntil == nil || !row.OpenUntil.After(now) {
		return 0, false
	}
	c.metrics.setCircuit(c.system, true)
	c.log.ErrorContext(ctx, "circuit breaker opened", logx.KeyOp, "Outbound.Circuit", "system", c.system,
		"failures", row.ConsecutiveFailures, "open_until", *row.OpenUntil)
	c.alerter.RaiseError(ctx, "circuit_open:"+c.system, "",
		fmt.Sprintf("Calls to %s suspended after %d consecutive failures", c.system, row.ConsecutiveFailures),
		map[string]any{"system": c.system, "failures": row.ConsecutiveFailures})
	return row.OpenUntil.Sub(now), true
}

// success обнуляет счётчик ошибок.
func (c circuit) success(ctx context.Context) {
	if err := sqlc.New(c.pool).ResetCircuit(context.WithoutCancel(ctx), sqlc.ResetCircuitParams{Now: c.clock.Now(), System: c.system}); err != nil {
		c.log.ErrorContext(ctx, "circuit update failed", logx.KeyOp, "Outbound.Circuit", "system", c.system, logx.KeyError, err.Error())
		return
	}
	c.metrics.setCircuit(c.system, false)
}
