package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
)

func (r *Users) SettingsAccess(ctx context.Context, user uuid.UUID) (domain.SettingsAccess, error) {
	return r.s.AccountSettings().Access(ctx, user)
}
