package inbound

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/inbound/sqlc"
)

// DeleteEvents удаляет обработанные и проигнорированные события старше before (R-10).
func DeleteEvents(pool *pgxpool.Pool) func(ctx context.Context, before time.Time, batch int32) (int64, error) {
	return func(ctx context.Context, before time.Time, batch int32) (int64, error) {
		return sqlc.New(pool).DeleteInboundEventsBefore(ctx, sqlc.DeleteInboundEventsBeforeParams{Before: before, Batch: batch})
	}
}
