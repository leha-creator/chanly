package job

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/job/sqlc"
)

// DeleteFinished удаляет задания в статусах statuses, завершённые раньше before (R-10).
func DeleteFinished(pool *pgxpool.Pool, statuses ...string) func(ctx context.Context, before time.Time, batch int32) (int64, error) {
	return func(ctx context.Context, before time.Time, batch int32) (int64, error) {
		return sqlc.New(pool).DeleteFinishedJobsBefore(ctx, sqlc.DeleteFinishedJobsBeforeParams{Statuses: statuses, Before: before, Batch: batch})
	}
}

// DeleteHeartbeats удаляет отметки экземпляров старше before (R-6).
func DeleteHeartbeats(pool *pgxpool.Pool) func(ctx context.Context, before time.Time, batch int32) (int64, error) {
	return func(ctx context.Context, before time.Time, _ int32) (int64, error) {
		return sqlc.New(pool).DeleteHeartbeatsBefore(ctx, before)
	}
}
