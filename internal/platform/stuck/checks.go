package stuck

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/stuck/sqlc"
)

// InboundEvents — входящее событие в received дольше threshold (07 §3: 10 мин).
func InboundEvents(pool *pgxpool.Pool, threshold time.Duration) Check {
	return Check{
		Name: "inbound_event", Threshold: threshold, Level: alert.LevelError,
		Find: func(ctx context.Context, olderThan time.Time) ([]string, error) {
			return sqlc.New(pool).StaleInboundEvents(ctx, olderThan)
		},
	}
}

// FailedJobs — задание в failed без оповещения (07 §3: сразу). Обычно
// оповещение поднимает сам обработчик при исчерпании попыток; проверка —
// запасной путь, если то оповещение не записалось.
func FailedJobs(pool *pgxpool.Pool) Check {
	return Check{
		Name: "failed_job", Threshold: 0, Level: alert.LevelError,
		Find: func(ctx context.Context, olderThan time.Time) ([]string, error) {
			return sqlc.New(pool).FailedJobsWithoutAlert(ctx, olderThan)
		},
	}
}
