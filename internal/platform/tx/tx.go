// Package tx — транзакции базы. Транзакция передаётся через context: все
// репозитории и job.Enqueue внутри InTx работают в ней, поэтому изменение
// данных и порождённые задания фиксируются вместе (I-10).
package tx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Runner выполняет fn в транзакции. Слой app зависит только от этого интерфейса.
type Runner interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}

type ctxKey struct{}

// FromContext возвращает транзакцию из context, если она открыта.
func FromContext(ctx context.Context) (pgx.Tx, bool) {
	t, ok := ctx.Value(ctxKey{}).(pgx.Tx)
	return t, ok
}

// Detach возвращает context без транзакции: запись в нём не откатится вместе с
// транзакцией вызывающего (журналы внешних вызовов, оповещения).
func Detach(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, nil)
}

// PgxRunner — Runner на пуле pgx. Уровень изоляции — READ COMMITTED.
type PgxRunner struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// NewRunner создаёт Runner.
func NewRunner(pool *pgxpool.Pool, log *slog.Logger) *PgxRunner {
	return &PgxRunner{pool: pool, log: log}
}

// InTx открывает транзакцию, кладёт её в context и вызывает fn. Ошибка или
// паника в fn откатывает транзакцию. Вложенный вызов использует внешнюю транзакцию.
func (r *PgxRunner) InTx(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	if _, ok := FromContext(ctx); ok {
		return fn(ctx)
	}
	t, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = t.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			if rbErr := t.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
				r.log.ErrorContext(ctx, "tx rollback failed", logx.KeyOp, "DB.InTx", logx.KeyError, rbErr.Error())
			}
			r.log.WarnContext(ctx, "tx rollback", logx.KeyOp, "DB.InTx", "reason", err.Error())
		}
	}()
	if err = fn(context.WithValue(ctx, ctxKey{}, t)); err != nil {
		return err
	}
	if err = t.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
