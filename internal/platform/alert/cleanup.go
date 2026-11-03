package alert

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/alert/sqlc"
)

// DeleteResolved удаляет закрытые оповещения, закрытые раньше before (R-10).
func DeleteResolved(pool *pgxpool.Pool) func(ctx context.Context, before time.Time, batch int32) (int64, error) {
	return func(ctx context.Context, before time.Time, batch int32) (int64, error) {
		return sqlc.New(pool).DeleteResolvedAlertsBefore(ctx, sqlc.DeleteResolvedAlertsBeforeParams{Before: before, Batch: batch})
	}
}
