package postgres

import (
	"context"
	"time"

	"github.com/leha-creator/chanly/internal/account/postgres/sqlc"
)

// DeleteExpiredAuthCodes удаляет коды, истёкшие раньше before (задание cleanup, R-10).
func (s *Store) DeleteExpiredAuthCodes(ctx context.Context, before time.Time, batch int32) (int64, error) {
	s.debug(ctx, "Account.DeleteExpiredAuthCodes", "before", before)
	return s.q(ctx).DeleteExpiredAuthCodes(ctx, sqlc.DeleteExpiredAuthCodesParams{Before: before, Batch: batch})
}

// DeleteExpiredSessions удаляет сессии, истёкшие раньше before (задание cleanup, R-10).
func (s *Store) DeleteExpiredSessions(ctx context.Context, before time.Time, batch int32) (int64, error) {
	s.debug(ctx, "Account.DeleteExpiredSessions", "before", before)
	return s.q(ctx).DeleteExpiredSessions(ctx, sqlc.DeleteExpiredSessionsParams{Before: before, Batch: batch})
}
