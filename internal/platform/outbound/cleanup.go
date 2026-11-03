package outbound

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/outbound/sqlc"
)

// DeleteRequests удаляет записи outbound_request старше before (04 §8: 180 суток).
func DeleteRequests(pool *pgxpool.Pool) func(ctx context.Context, before time.Time, batch int32) (int64, error) {
	return func(ctx context.Context, before time.Time, batch int32) (int64, error) {
		return sqlc.New(pool).DeleteOutboundRequestsBefore(ctx, sqlc.DeleteOutboundRequestsBeforeParams{Before: before, Batch: batch})
	}
}
